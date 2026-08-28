package flags

import "time"

// BrowserFlags contains the command-line values for the session browser
// capability. Zero values intentionally do not mean configuration defaults:
// callers must use Cobra's Changed bit before applying a value so an omitted
// flag cannot clear a YAML or environment setting.
type BrowserFlags struct {
	Tools              string
	CDPURL             string
	WSEndpoint         string
	UserDataDir        string
	AllowProcessScan   bool
	AllowRemoteCDP     bool
	Browser            string
	Tab                string
	Origin             string
	AutoSelect         string
	ActivateTab        bool
	PersistSelection   bool
	AllowedOrigins     []string
	DeniedOrigins      []string
	Approval           string
	CancelOnInterrupt  string
	InvocationTimeout  time.Duration
	MaxInputBytes      int
	MaxResultBytes     int
	SerializePerTarget bool
	Record             bool
	RecordArguments    bool
	RecordResults      bool
	RedactURLQuery     bool
	RedactURLFragment  bool
	Replay             string
	ReplayStrict       bool
}

// NewBrowserFlags returns an empty presence-aware browser flag value.
func NewBrowserFlags() *BrowserFlags { return &BrowserFlags{} }
