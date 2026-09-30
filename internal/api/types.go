package api

// CLIConfig is GET /api/v1/agent/cli-config: how to log in.
type CLIConfig struct {
	ClientID           string   `json:"client_id"`
	AuthorizeEndpoint  string   `json:"authorize_endpoint"`
	TokenEndpoint      string   `json:"token_endpoint"`
	RevocationEndpoint string   `json:"revocation_endpoint"`
	Resource           string   `json:"resource"`
	Scope              string   `json:"scope"`
	LoopbackRedirects  []string `json:"loopback_redirects"`
	CodeRedirect       string   `json:"code_redirect"`
	DashboardTokensURL string   `json:"dashboard_tokens_url"`
}

// Me is GET /me.
type Me struct {
	UserID  string `json:"user_id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Plan    string `json:"plan"`
	Credits *int64 `json:"credits"`
	Agent   struct {
		Name string `json:"name"`
	} `json:"agent"`
	Connection struct {
		Surface string `json:"surface"`
		Client  string `json:"client"`
	} `json:"connection"`
}

// File is a deliverable: a signed link pinned to one revision.
type File struct {
	ID       string  `json:"id"`
	WorkID   string  `json:"work_id"`
	FileName string  `json:"file_name"`
	Kind     string  `json:"kind"`
	Mime     string  `json:"mime"`
	URL      string  `json:"url"`
	Label    string  `json:"label"`
	Revision *string `json:"revision"`
	Size     *int64  `json:"size"`
}

// Rev is the file's revision, "" when it has none (a site).
func (f File) Rev() string {
	if f.Revision == nil {
		return ""
	}
	return *f.Revision
}

// WorkRef is a piece of work a message started.
type WorkRef struct {
	WorkID string `json:"work_id"`
	Title  string `json:"title"`
	Link   string `json:"link"`
}

// Turn is POST /messages.
type Turn struct {
	RequestID         string    `json:"request_id"`
	Status            string    `json:"status"` // answered | processing | busy | failed
	Reply             *string   `json:"reply"`
	Work              []WorkRef `json:"work"`
	Notes             []string  `json:"notes"`
	Cursor            string    `json:"cursor"`
	Files             []File    `json:"files"`
	RetryAfterSeconds int       `json:"retry_after_seconds"`
	Error             string    `json:"error"`
}

// Entry is one update-log entry.
type Entry struct {
	Seq       int64    `json:"seq"`
	Kind      string   `json:"kind"` // result | state | progress | agent_reply | turn_failed | turn_busy
	WorkID    string   `json:"work_id"`
	RequestID string   `json:"request_id"`
	At        string   `json:"at"` // server time, ISO 8601 (no zone: UTC)
	Title     string   `json:"title"`
	Text      string   `json:"text"`
	Outcome   string   `json:"outcome"`
	Step      string   `json:"step"`
	Status    string   `json:"status"`
	FileNames []string `json:"file_names"`
	Files     []File   `json:"files"`
}

// WorkState is a piece of work's current state.
type WorkState struct {
	WorkID string `json:"work_id"`
	State  string `json:"state"`
}

// Poll is GET /updates.
type Poll struct {
	Entries       []Entry     `json:"entries"`
	Work          []WorkState `json:"work"`
	More          bool        `json:"more"`
	Done          bool        `json:"done"`
	Cursor        string      `json:"cursor"`
	CursorExpired bool        `json:"cursor_expired"`
}

// Work is GET /work/{id}.
type Work struct {
	WorkID       string `json:"work_id"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	Text         string `json:"text"`
	NextFromChar *int   `json:"next_from_char"`
	Link         string `json:"link"`
	Files        []File `json:"files"`
}

// WorkListItem is one row of GET /work.
type WorkListItem struct {
	WorkID  string `json:"work_id"`
	Title   string `json:"title"`
	Preview string `json:"preview"`
	Link    string `json:"link"`
}

// Recovery is POST /turns/{id}/recover.
type Recovery struct {
	RequestID string `json:"request_id"`
	Work      []struct {
		WorkID  string `json:"work_id"`
		State   string `json:"state"` // settled | watching | recovering | exhausted
		Outcome string `json:"outcome"`
	} `json:"work"`
}

// Upload is POST /uploads.
type Upload struct {
	UploadID string            `json:"upload_id"`
	URL      string            `json:"url"`
	Fields   map[string]string `json:"fields"`
	Status   string            `json:"status"`
	Size     *int64            `json:"size"`
}

// FileIn is an attachment sent inline with a message.
type FileIn struct {
	Name        string `json:"name,omitempty"`
	Base64      string `json:"base64,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// MessageIn is the POST /messages body.
type MessageIn struct {
	Message   string   `json:"message"`
	RequestID string   `json:"request_id"`
	Files     []FileIn `json:"files,omitempty"`
	UploadIDs []string `json:"upload_ids,omitempty"`
}
