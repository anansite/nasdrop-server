package db

type PhotoMetadata struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Dir       string `json:"dir"`
	Size      int64  `json:"size"`
	Modified  int64  `json:"modified"`
	TakenAt   int64  `json:"taken_at"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	HasThumb  bool   `json:"has_thumb"`
	UpdatedAt int64  `json:"updated_at"`
}

type TimelineBucket struct {
	DateKey string `json:"date_key"` // e.g. "2024-05-14"
	Count   int    `json:"count"`
}
