package httpapi_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyroynardson/watermarker/apps/api/internal/auth"
	"github.com/rickyroynardson/watermarker/apps/api/internal/batch"
	"github.com/rickyroynardson/watermarker/apps/api/internal/httpapi"
	"github.com/rickyroynardson/watermarker/apps/api/internal/storage"
	"github.com/stretchr/testify/require"
)

// A local provider exercises real discovery, PKCE exchange, JWKS and signature validation.
func testOIDC(t *testing.T, db *pgxpool.Pool, objects *storage.S3) {
	ctx := t.Context()
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	encode := func(v any) string {
		b, e := json.Marshal(v)
		require.NoError(t, e)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	var issuer, nonce, challenge, mode string
	subject := "alice"
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "test", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(private.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(private.E)).Bytes())}}})
		case "/token":
			if err := r.ParseForm(); err != nil {
				w.WriteHeader(400)
				return
			}
			proof := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("code") != "test-code" || base64.RawURLEncoding.EncodeToString(proof[:]) != challenge {
				w.WriteHeader(400)
				return
			}
			claims := map[string]any{"iss": issuer, "aud": "test-client", "sub": subject, "nonce": nonce, "name": "Test User", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
			switch mode {
			case "audience":
				claims["aud"] = "other-client"
			case "issuer":
				claims["iss"] = "https://other.example"
			case "nonce":
				claims["nonce"] = "wrong"
			case "expired":
				claims["exp"] = time.Now().Add(-time.Hour).Unix()
			case "subject":
				claims["sub"] = ""
			}
			payload := encode(map[string]string{"alg": "RS256", "kid": "test"}) + "." + encode(claims)
			sum := sha256.Sum256([]byte(payload))
			sig, e := rsa.SignPKCS1v15(rand.Reader, private, crypto.SHA256, sum[:])
			if e != nil {
				w.WriteHeader(500)
				return
			}
			if mode == "signature" {
				sig[0] ^= 1
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "unused", "token_type": "Bearer", "id_token": payload + "." + base64.RawURLEncoding.EncodeToString(sig)})
		default:
			w.WriteHeader(404)
		}
	}))
	defer provider.Close()
	issuer = provider.URL
	const origin = "http://localhost:5173"
	login, err := auth.NewLogin(ctx, db, issuer, "test-client", "test-secret", origin)
	require.NoError(t, err)
	router := httpapi.NewRouter(db, objects, login)
	call := func(method, path, body string, cookie *http.Cookie, originHeader, bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if originHeader != "" {
			req.Header.Set("Origin", originHeader)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	start := func() (string, *http.Cookie) {
		w := call("GET", "/auth/login", "", nil, "", "")
		require.Equal(t, 302, w.Code)
		redirect, e := url.Parse(w.Header().Get("Location"))
		require.NoError(t, e)
		require.Equal(t, "S256", redirect.Query().Get("code_challenge_method"))
		require.Equal(t, origin+"/api/auth/callback", redirect.Query().Get("redirect_uri"))
		nonce, challenge = redirect.Query().Get("nonce"), redirect.Query().Get("code_challenge")
		require.NotEmpty(t, nonce)
		return "/auth/callback?code=test-code&state=" + redirect.Query().Get("state"), w.Result().Cookies()[0]
	}
	config := call("GET", "/auth/config", "", nil, "", "")
	require.Equal(t, 200, config.Code)
	require.Contains(t, config.Body.String(), origin+"/api/auth/login")
	callback, cookie := start()
	missingCookie := call("GET", callback, "", nil, "", "")
	require.Equal(t, 400, missingCookie.Code, "browser binding required")
	require.Contains(t, missingCookie.Body.String(), "invalid_login")
	require.Contains(t, missingCookie.Body.String(), origin)
	good := call("GET", callback, "", cookie, "", "")
	require.Equal(t, 303, good.Code)
	require.Equal(t, origin+"/", good.Header().Get("Location"))
	replayed := call("GET", callback, "", cookie, "", "")
	require.Equal(t, 400, replayed.Code, "callback cannot replay")
	require.Contains(t, replayed.Body.String(), "expired_login")
	var session *http.Cookie
	for _, c := range good.Result().Cookies() {
		if c.Name == "watermarker_session" {
			session = c
		}
	}
	require.NotNil(t, session)
	require.True(t, session.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, session.SameSite)
	require.Equal(t, 8*60*60, session.MaxAge)
	me := call("GET", "/auth/me", "", session, "", "")
	require.Equal(t, 200, me.Code)
	var identity struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(me.Body.Bytes(), &identity))
	userID := identity.Data.ID
	// A fresh login for the same provider identity keeps its user and data namespace.
	repeatPath, repeatCookie := start()
	repeat := call("GET", repeatPath, "", repeatCookie, "", "")
	var repeatSession *http.Cookie
	for _, c := range repeat.Result().Cookies() {
		if c.Name == "watermarker_session" {
			repeatSession = c
		}
	}
	require.NotNil(t, repeatSession)
	var repeated struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(call("GET", "/auth/me", "", repeatSession, "", "").Body.Bytes(), &repeated))
	require.Equal(t, userID, repeated.Data.ID)
	require.NotEqual(t, uuid.Nil, userID)
	for _, badOrigin := range []string{"", "https://attacker.example"} {
		require.Equal(t, 403, call("POST", "/uploads/presign", `{"content_type":"image/png"}`, session, badOrigin, "").Code)
	}
	require.Equal(t, 401, call("GET", "/batches", "", session, "", "invalid-key").Code, "no fallback from invalid bearer")
	upload := call("POST", "/uploads/presign", `{"content_type":"image/png"}`, session, origin, "")
	require.Equal(t, 200, upload.Code)
	require.Contains(t, upload.Body.String(), "uploads/"+userID.String()+"/")
	makeKey := func() (uuid.UUID, string) {
		w := call("POST", "/auth/keys", `{"name":"script"}`, session, origin, "")
		require.Equal(t, 201, w.Code)
		var result struct {
			Data struct {
				ID  uuid.UUID `json:"id"`
				Key string    `json:"key"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
		return result.Data.ID, result.Data.Key
	}
	keyID, rawKey := makeKey()
	_, secondKey := makeKey()
	b := batch.Batch{ID: uuid.New(), UserID: userID, WatermarkKey: "sources/" + userID.String() + "/" + uuid.NewString(), Images: []batch.Image{{ID: uuid.New(), SourceKey: "sources/" + userID.String() + "/" + uuid.NewString()}}}
	_, err = batch.NewRepository(db).CreateBatch(ctx, b)
	require.NoError(t, err)
	defer db.Exec(ctx, "DELETE FROM batches WHERE id=$1", b.ID)
	path := "/batches/" + b.ID.String()
	require.Equal(t, 200, call("GET", path, "", nil, "", rawKey).Code)
	require.Equal(t, 200, call("GET", path, "", nil, "", secondKey).Code)
	require.Equal(t, 403, call("GET", "/auth/keys", "", nil, "", rawKey).Code)
	require.Equal(t, 200, call("DELETE", "/auth/keys/"+keyID.String(), "", session, origin, "").Code)
	require.Equal(t, 401, call("GET", path, "", nil, "", rawKey).Code)
	require.Equal(t, 200, call("GET", path, "", session, "", "").Code)
	// Different provider subject gets a separate owner even with the same display name.
	subject = "bob"
	callback, cookie = start()
	bobResponse := call("GET", callback, "", cookie, "", "")
	require.Equal(t, 303, bobResponse.Code)
	var bob *http.Cookie
	for _, c := range bobResponse.Result().Cookies() {
		if c.Name == "watermarker_session" {
			bob = c
		}
	}
	require.NotNil(t, bob)
	require.Equal(t, 404, call("GET", path, "", bob, "", "").Code)
	require.Equal(t, 404, call("POST", path+"/cancel", "", bob, origin, "").Code)
	require.Equal(t, 404, call("DELETE", "/auth/keys/"+keyID.String(), "", bob, origin, "").Code)
	require.Equal(t, 404, call("POST", path+"/images/"+b.Images[0].ID.String()+"/retry", `{"attempt":0}`, bob, origin, "").Code)
	require.Equal(t, 200, call("POST", "/auth/logout", "", session, origin, "").Code)
	require.Equal(t, 401, call("GET", path, "", session, "", "").Code)
	_, err = db.Exec(ctx, "UPDATE auth_sessions SET expires_at=now()-interval '1 second'")
	require.NoError(t, err)
	require.Equal(t, 401, call("GET", "/auth/me", "", bob, "", "").Code)
	require.Equal(t, 403, call("POST", "/auth/logout", "", bob, "https://attacker.example", "").Code)
	require.Equal(t, 200, call("POST", "/auth/logout", "", bob, origin, "").Code)
	callback, cookie = start()
	_, err = db.Exec(ctx, "UPDATE auth_logins SET expires_at=now()-interval '1 second'")
	require.NoError(t, err)
	require.Equal(t, 400, call("GET", callback, "", cookie, "", "").Code)
	for _, bad := range []string{"audience", "issuer", "nonce", "expired", "subject", "signature"} {
		mode = bad
		callback, cookie = start()
		w := call("GET", callback, "", cookie, "", "")
		require.Equal(t, origin+"/?auth_error=1", w.Header().Get("Location"), bad)
		for _, c := range w.Result().Cookies() {
			require.NotEqual(t, "watermarker_session", c.Name, bad)
		}
	}
}
