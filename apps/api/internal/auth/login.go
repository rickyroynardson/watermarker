package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
	"golang.org/x/oauth2"
)

type Login struct {
	db       *pgxpool.Pool
	origin   string
	secure   bool
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// NewLogin is optional; a partial configuration fails startup rather than silently disabling login.
func NewLogin(ctx context.Context, db *pgxpool.Pool, issuer, clientID, secret, origin string) (*Login, error) {
	if issuer == "" && clientID == "" && secret == "" && origin == "" {
		return nil, nil
	}
	if issuer == "" || clientID == "" || origin == "" {
		return nil, errors.New("OIDC_ISSUER_URL, OIDC_CLIENT_ID and APP_ORIGIN must all be configured")
	}
	for _, raw := range []string{origin, issuer} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("invalid authentication URL")
		}
		local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
		if u.Scheme != "https" && !(u.Scheme == "http" && local) {
			return nil, errors.New("authentication requires HTTPS except on loopback")
		}
		if raw == origin && u.Path != "" {
			return nil, errors.New("APP_ORIGIN must be an origin without a path or trailing slash")
		}
	}
	// A bounded HTTP client also applies to subsequent key refreshes and code exchange.
	ctx = oidc.ClientContext(ctx, &http.Client{Timeout: 10 * time.Second})
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery: %w", err)
	}
	return &Login{db: db, origin: origin, secure: strings.HasPrefix(origin, "https://"),
		oauth:    oauth2.Config{ClientID: clientID, ClientSecret: secret, Endpoint: provider.Endpoint(), RedirectURL: origin + "/api/auth/callback", Scopes: []string{oidc.ScopeOpenID, "profile"}},
		verifier: provider.Verifier(&oidc.Config{ClientID: clientID}),
	}, nil
}

func FromEnv(ctx context.Context, db *pgxpool.Pool) (*Login, error) {
	return NewLogin(ctx, db, os.Getenv("OIDC_ISSUER_URL"), os.Getenv("OIDC_CLIENT_ID"), os.Getenv("OIDC_CLIENT_SECRET"), os.Getenv("APP_ORIGIN"))
}

func (l *Login) cookie(c *gin.Context, name, value string, age int) {
	http.SetCookie(c.Writer, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: l.secure, SameSite: http.SameSiteLaxMode, MaxAge: age})
}

func (l *Login) start(c *gin.Context) {
	// Block explicit cross-site login initiation; top-level navigation without Origin is allowed.
	if origin := c.GetHeader("Origin"); origin != "" && origin != l.origin {
		c.AbortWithStatus(403)
		return
	}
	if c.GetHeader("Sec-Fetch-Site") == "cross-site" {
		c.AbortWithStatus(403)
		return
	}
	state, nonce, verifier := rand.Text(), rand.Text(), oauth2.GenerateVerifier()
	ctx := c.Request.Context()
	if _, err := l.db.Exec(ctx, "DELETE FROM auth_logins WHERE expires_at<now(); DELETE FROM auth_sessions WHERE expires_at<now()"); err != nil {
		c.AbortWithStatus(500)
		return
	}
	_, err := l.db.Exec(ctx, "INSERT INTO auth_logins(state_hash,verifier,nonce,expires_at) VALUES($1,$2,$3,now()+interval '10 minutes')", hash(state), verifier, nonce)
	if err != nil {
		c.AbortWithStatus(500)
		return
	}
	l.cookie(c, loginCookie, state, 600)
	c.Redirect(302, l.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)))
}

func (l *Login) callback(c *gin.Context) {
	state := c.Query("state")
	cookie, err := c.Cookie(loginCookie)
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(cookie), []byte(state)) != 1 {
		utils.RespondError(c, 400, "invalid_login", "Login could not be matched to this browser. Start again using Sign in at "+l.origin+". Use the same hostname throughout and allow this site's cookies.")
		return
	}
	l.cookie(c, loginCookie, "", -1)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	var verifier, nonce string
	err = l.db.QueryRow(ctx, "DELETE FROM auth_logins WHERE state_hash=$1 AND expires_at>now() RETURNING verifier,nonce", hash(state)).Scan(&verifier, &nonce)
	if errors.Is(err, pgx.ErrNoRows) {
		utils.RespondError(c, 400, "expired_login", "This login attempt expired or was already used. Start again using Sign in.")
		return
	}
	if err != nil {
		utils.RespondError(c, 500, utils.CodeInternal, "Could not check this login attempt. Please try again.")
		return
	}
	fail := func() { c.Redirect(303, l.origin+"/?auth_error=1") }
	if c.Query("error") != "" || c.Query("code") == "" {
		fail()
		return
	}
	ctx = oidc.ClientContext(ctx, &http.Client{Timeout: 10 * time.Second})
	token, err := l.oauth.Exchange(ctx, c.Query("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		fail()
		return
	}
	raw, _ := token.Extra("id_token").(string)
	identity, err := l.verifier.Verify(ctx, raw)
	if err != nil || identity.Subject == "" || identity.Nonce != nonce {
		fail()
		return
	}
	var claims struct {
		Name string `json:"name"`
	}
	if err := identity.Claims(&claims); err != nil {
		fail()
		return
	}
	if claims.Name == "" {
		claims.Name = "User"
	}
	if len(claims.Name) > 200 {
		claims.Name = "User"
	}
	tx, err := l.db.Begin(ctx)
	if err != nil {
		c.AbortWithStatus(500)
		return
	}
	defer tx.Rollback(ctx)
	var id uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO users(id,issuer,subject,name) VALUES($1,$2,$3,$4)
 ON CONFLICT(issuer,subject) DO UPDATE SET name=EXCLUDED.name RETURNING id`, uuid.New(), identity.Issuer, identity.Subject, claims.Name).Scan(&id)
	if err != nil {
		c.AbortWithStatus(500)
		return
	}
	// Rotate the browser session after every successful login.
	if old, err := c.Cookie(sessionCookie); err == nil {
		if _, err = tx.Exec(ctx, "DELETE FROM auth_sessions WHERE token_hash=$1", hash(old)); err != nil {
			c.AbortWithStatus(500)
			return
		}
	}
	session := rand.Text()
	_, err = tx.Exec(ctx, "INSERT INTO auth_sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '8 hours')", hash(session), id)
	if err != nil {
		c.AbortWithStatus(500)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		c.AbortWithStatus(500)
		return
	}
	l.cookie(c, sessionCookie, session, 8*60*60)
	c.Redirect(303, l.origin+"/")
}

func Register(r *gin.Engine, db *pgxpool.Pool, login *Login) gin.HandlerFunc {
	origin := ""
	if login != nil {
		origin = login.origin
	}
	require := RequireUser(db, origin)
	group := r.Group("/auth", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Referrer-Policy", "no-referrer")
		c.Next()
	})
	group.GET("/config", func(c *gin.Context) {
		loginURL := ""
		if login != nil {
			loginURL = login.origin + "/api/auth/login"
		}
		utils.RespondSuccess(c, 200, gin.H{"oidc_enabled": login != nil, "login_url": loginURL})
	})
	if login != nil {
		group.GET("/login", login.start)
		group.GET("/callback", login.callback)
		group.POST("/logout", func(c *gin.Context) {
			// Clearing an expired session must work too, but never from another origin.
			if c.GetHeader("Origin") != login.origin {
				c.AbortWithStatus(403)
				return
			}
			token, _ := c.Cookie(sessionCookie)
			if _, err := db.Exec(c.Request.Context(), "DELETE FROM auth_sessions WHERE token_hash=$1", hash(token)); err != nil {
				c.AbortWithStatus(500)
				return
			}
			login.cookie(c, sessionCookie, "", -1)
			utils.RespondSuccess(c, 200, gin.H{"signed_out": true})
		})
	}
	group.GET("/me", require, func(c *gin.Context) {
		var name string
		if err := db.QueryRow(c.Request.Context(), "SELECT name FROM users WHERE id=$1", UserID(c)).Scan(&name); err != nil {
			c.AbortWithStatus(500)
			return
		}
		utils.RespondSuccess(c, 200, gin.H{"id": UserID(c), "name": name})
	})
	registerKeys(group, db, require)
	return require
}
