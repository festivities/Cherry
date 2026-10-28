package store

// DiaryProfile is the Diary profile edit record (web/profileedit.go): the owner's text fields and
// whether each one is secret from other visitors. Replaced, never edited in place. StatusMessage is
// the profile Status Message (saved by POST /v4/avatar/<aid>/profile as "intro"); it is public.
type DiaryProfile struct {
	StatusMessage   string `json:"statusMessage,omitempty"`
	Location        string `json:"location,omitempty"`
	LocationSecret  bool   `json:"locationSecret,omitempty"`
	Sex             string `json:"sex,omitempty"`
	SexSecret       bool   `json:"sexSecret,omitempty"`
	BirthDay        string `json:"birthDay,omitempty"`
	BirthDaySecret  bool   `json:"birthDaySecret,omitempty"`
	BloodType       string `json:"bloodType,omitempty"`
	BloodTypeSecret bool   `json:"bloodTypeSecret,omitempty"`
	Job             string `json:"job,omitempty"`
	JobSecret       bool   `json:"jobSecret,omitempty"`
	// Photos is the profile photo list (POST /v4/profile/image), in the client's order.
	Photos []ProfilePhoto `json:"photos,omitempty"`
}

// ProfilePhoto is one profile photo row: the client's image id and URL and whether it is the main photo.
type ProfilePhoto struct {
	ImageID  string `json:"imageId"`
	ImageURL string `json:"imageUrl"`
	Main     bool   `json:"mainImage"`
}
