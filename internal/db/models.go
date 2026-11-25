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
	HasThumb        bool    `json:"has_thumb"`
	ThumbID         string  `json:"thumb_id"`
	VoiceID         string  `json:"voice_id"`
	VoiceDurationMs int64   `json:"voice_duration_ms"`
	ClearVoice      bool    `json:"clear_voice,omitempty"`
	Memo            string  `json:"memo"`
	ClearMemo       bool    `json:"clear_memo,omitempty"`
	Lat             float64 `json:"lat"`
	Lng             float64 `json:"lng"`
	LocationName    string  `json:"location_name"`
	UpdatedAt       int64   `json:"updated_at"`
}

type TimelineBucket struct {
	DateKey string `json:"date_key"` // e.g. "2024-05-14"
	Count   int    `json:"count"`
}

type PhotoGroup struct {
	ID        string   `json:"id"`
	AlbumDir  string   `json:"album_dir"`
	Title     string   `json:"title"`
	Memo      string   `json:"memo"`
	Items     []string `json:"items"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
}
