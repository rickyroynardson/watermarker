package upload

type PresignRequest struct {
	SizeBytes   int64  `json:"size_bytes" validate:"min=0,max=10485760"`
	ContentType string `json:"content_type" validate:"required,oneof=image/jpeg image/png image/webp"`
}
