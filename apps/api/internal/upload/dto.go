package upload

type PresignRequest struct {
	ContentType string `json:"content_type" validate:"required,oneof=image/jpeg image/png image/webp"`
}
