package quota

import (
	"context"
	"errors"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyroynardson/watermarker/apps/api/internal/auth"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
)

var ErrExceeded = errors.New("storage quota exceeded")

// Source reservations and stored outputs count; temporary staging copies are excluded.
// Reservations count until the persistent object is deleted, including abandoned uploads.
// ponytail: sum indexed account reservations; use a transactional counter if history makes reads slow.
const usage = `SELECT (COALESCE(sum(r.bytes),0)+(SELECT COALESCE(sum(o.bytes),0) FROM output_storage o
 WHERE o.user_id=$1 AND NOT EXISTS(SELECT 1 FROM cleanup_objects c WHERE c.key=o.key AND c.deleted_at IS NOT NULL)))::bigint FROM upload_reservations r
 WHERE r.user_id=$1 AND NOT EXISTS(SELECT 1 FROM cleanup_objects c WHERE c.key=r.key AND c.deleted_at IS NOT NULL
 AND (EXISTS(SELECT 1 FROM batches b WHERE b.watermark_key=r.key) OR EXISTS(SELECT 1 FROM images i WHERE i.source_key=r.key) OR EXISTS(SELECT 1 FROM cleanup_objects staging WHERE staging.key=replace(r.key,'sources/','uploads/') AND staging.deleted_at IS NOT NULL)))`

type Account struct {
	DemoEnabled bool   `json:"demo_enabled"`
	Plan        string `json:"plan"`
	Included    int64  `json:"included_bytes"`
	Addon       int64  `json:"addon_bytes"`
	Used        int64  `json:"used_bytes"`
}

func Reserve(ctx context.Context, db *pgxpool.Pool, owner uuid.UUID, key string, bytes int64) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var limit int64
	// Lock the account before reading usage: independent API instances share this admission guard.
	if err = tx.QueryRow(ctx, `SELECT p.included_bytes+u.addon_bytes FROM users u JOIN quota_plans p ON p.name=u.quota_plan WHERE u.id=$1 FOR UPDATE OF u`, owner).Scan(&limit); err != nil {
		return err
	}
	var used int64
	if err = tx.QueryRow(ctx, usage, owner).Scan(&used); err != nil {
		return err
	}
	if bytes < 1 || bytes > 10485760 || used > limit-bytes {
		return ErrExceeded
	}
	if _, err = tx.Exec(ctx, "INSERT INTO upload_reservations(key,user_id,bytes) VALUES($1,$2,$3)", key, owner, bytes); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func Register(r *gin.Engine, db *pgxpool.Pool, require gin.HandlerFunc) {
	r.GET("/account/quota", require, func(c *gin.Context) {
		var a Account
		err := db.QueryRow(c.Request.Context(), `SELECT u.quota_plan,p.included_bytes,u.addon_bytes,(`+usage+`) FROM users u JOIN quota_plans p ON p.name=u.quota_plan WHERE u.id=$1`, auth.UserID(c)).Scan(&a.Plan, &a.Included, &a.Addon, &a.Used)
		if err != nil {
			utils.RespondError(c, 500, utils.CodeInternal, "Could not load quota.")
			return
		}
		a.DemoEnabled = os.Getenv("QUOTA_DEMO_ENABLED") == "true"
		utils.RespondSuccess(c, 200, a)
	})
	// Demo grants are intentionally available to the authenticated owner; no payment is collected.
	r.POST("/account/quota/demo", require, func(c *gin.Context) {
		if os.Getenv("QUOTA_DEMO_ENABLED") != "true" {
			utils.RespondError(c, 403, "demo_disabled", "Demo plan changes are disabled.")
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
		var body struct {
			Plan  string `json:"plan"`
			Addon bool   `json:"addon"`
		}
		if c.ShouldBindJSON(&body) != nil || (body.Plan != "free" && body.Plan != "pro" && !body.Addon) || (body.Addon && body.Plan != "") {
			utils.RespondError(c, 400, utils.CodeInvalidRequest, "Choose a plan or an extra 100 MiB allowance.")
			return
		}
		var err error
		if body.Addon {
			id, parseErr := uuid.Parse(c.GetHeader("Idempotency-Key"))
			if parseErr != nil {
				utils.RespondError(c, 400, utils.CodeInvalidRequest, "An Idempotency-Key UUID is required for extra storage.")
				return
			}
			tx, e := db.Begin(c.Request.Context())
			if e != nil {
				utils.RespondError(c, 500, utils.CodeInternal, "Could not update quota.")
				return
			}
			defer tx.Rollback(c.Request.Context())
			_, err = tx.Exec(c.Request.Context(), "SELECT id FROM users WHERE id=$1 FOR UPDATE", auth.UserID(c))
			if err == nil {
				tag, e := tx.Exec(c.Request.Context(), "INSERT INTO quota_addons(user_id,request_id) VALUES($1,$2) ON CONFLICT DO NOTHING", auth.UserID(c), id)
				err = e
				if err == nil && tag.RowsAffected() > 0 {
					tag, err = tx.Exec(c.Request.Context(), "UPDATE users SET addon_bytes=addon_bytes+104857600 WHERE id=$1 AND addon_bytes<=10737418240-104857600", auth.UserID(c))
					if err == nil && tag.RowsAffected() == 0 {
						utils.RespondError(c, 409, "quota_limit", "Maximum demo allowance reached.")
						return
					}
				}
			}
			if err == nil {
				err = tx.Commit(c.Request.Context())
			}
		} else {
			_, err = db.Exec(c.Request.Context(), "UPDATE users SET quota_plan=$2 WHERE id=$1", auth.UserID(c), body.Plan)
		}
		if err != nil {
			utils.RespondError(c, 500, utils.CodeInternal, "Could not update quota.")
			return
		}
		utils.RespondSuccess(c, 200, gin.H{"updated": true})
	})
}
