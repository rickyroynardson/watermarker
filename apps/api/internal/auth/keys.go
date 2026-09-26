package auth

import (
	"crypto/rand"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
)

func registerKeys(group *gin.RouterGroup, db *pgxpool.Pool, require gin.HandlerFunc) {
	keys := group.Group("/keys", require, sessionOnly)
	keys.GET("", func(c *gin.Context) {
		rows, err := db.Query(c.Request.Context(), "SELECT id,name,created_at FROM api_keys WHERE user_id=$1 AND revoked_at IS NULL ORDER BY created_at,id", UserID(c))
		if err != nil {
			c.AbortWithStatus(500)
			return
		}
		defer rows.Close()
		type key struct {
			ID        uuid.UUID `json:"id"`
			Name      string    `json:"name"`
			CreatedAt time.Time `json:"created_at"`
		}
		result, err := pgx.CollectRows(rows, pgx.RowToStructByName[key])
		if err != nil {
			c.AbortWithStatus(500)
			return
		}
		if result == nil {
			result = []key{}
		}
		utils.RespondSuccess(c, 200, result)
	})
	keys.POST("", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
		var body struct {
			Name string `json:"name"`
		}
		if c.ShouldBindJSON(&body) != nil || len(strings.TrimSpace(body.Name)) == 0 || len(body.Name) > 100 {
			c.AbortWithStatus(400)
			return
		}
		id, token := uuid.New(), rand.Text()
		_, err := db.Exec(c.Request.Context(), "INSERT INTO api_keys(id,user_id,name,key_hash) VALUES($1,$2,$3,$4)", id, UserID(c), strings.TrimSpace(body.Name), hash(token))
		if err != nil {
			c.AbortWithStatus(500)
			return
		}
		utils.RespondSuccess(c, 201, gin.H{"id": id, "key": token})
	})
	keys.DELETE("/:id", func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.AbortWithStatus(400)
			return
		}
		tag, err := db.Exec(c.Request.Context(), "UPDATE api_keys SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1 AND user_id=$2", id, UserID(c))
		if err != nil {
			c.AbortWithStatus(500)
			return
		}
		if tag.RowsAffected() == 0 {
			c.AbortWithStatus(404)
			return
		}
		utils.RespondSuccess(c, 200, gin.H{"revoked": true})
	})
}
