package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// SettingSystem holds System as a JSON object.
const SettingSystem = "system"

// Ways users are told apart, System.AuthType.
const (
	// AuthForm asks for a name and a password.
	AuthForm = "form"
	// AuthHTTP takes the name from the reverse proxy in front of the server.
	AuthHTTP = "http_auth"
	// AuthNone lets everybody in as the default user.
	AuthNone = "none"
)

// System is what an administrator sets for the whole installation. The
// fields are named as in the config.php of FreshRSS, which import reads them
// from; a field that is not stored has the default of FreshRSS.
type System struct {
	// Title is what the installation calls itself in the interface.
	Title string `json:"title"`
	// Language is the interface language of visitors that state no
	// preference of their own.
	Language string `json:"language"`
	// DefaultUser is the user anonymous visitors read as, and an
	// administrator whatever their own settings say. Empty until the first
	// user is created.
	DefaultUser string `json:"default_user"`
	AuthType    string `json:"auth_type"`
	// AllowAnonymous lets visitors who are not logged in read the entries
	// of the default user; AllowAnonymousRefresh also lets them refresh the
	// feeds.
	AllowAnonymous        bool `json:"allow_anonymous"`
	AllowAnonymousRefresh bool `json:"allow_anonymous_refresh"`
	// APIEnabled switches the Google Reader API and the public feeds of
	// saved queries on.
	APIEnabled bool `json:"api_enabled"`
	// ForceEmailValidation keeps a user out until they have followed the
	// link sent to their address.
	ForceEmailValidation bool `json:"force_email_validation"`
	// HTTPAuthAutoRegister creates a user for a name the reverse proxy
	// vouches for and nobody has yet.
	HTTPAuthAutoRegister bool `json:"http_auth_auto_register"`
	// ReauthTime is how many seconds after typing the password an
	// administrator may act without typing it again.
	ReauthTime int `json:"reauth_time"`
	// ClosedRegistrationMessage is shown to visitors when nobody can
	// register any more.
	ClosedRegistrationMessage string `json:"closed_registration_message"`
	Limits                    Limits `json:"limits"`
	// TOS are the terms a visitor has to accept to register, as HTML; empty
	// for none. FreshRSS keeps them in the file data/tos.html.
	TOS string `json:"tos"`
	// Proxy is the address of the proxy feeds are fetched through unless
	// they have their own or are set to go through none: a URL with the
	// scheme http, https, socks5 or socks5h. Empty for none.
	Proxy string `json:"proxy"`
}

// Limits bound what users may take up.
type Limits struct {
	// CookieDuration is how many seconds a visitor who asked to be
	// remembered stays logged in without coming back.
	CookieDuration int `json:"cookie_duration"`
	MaxFeeds       int `json:"max_feeds"`
	MaxCategories  int `json:"max_categories"`
	// MaxRegistrations is the number of users from which on nobody can
	// register on their own; zero is no limit, one closes registration.
	MaxRegistrations int `json:"max_registrations"`
}

// DefaultSystem returns the settings of an installation nobody has
// configured: those of FreshRSS, except that the API is on, since it is what
// freshgo has been serving all along.
func DefaultSystem() System {
	return System{
		Title:                "freshgo",
		Language:             "en",
		AuthType:             AuthForm,
		APIEnabled:           true,
		HTTPAuthAutoRegister: true,
		ReauthTime:           1200,
		Limits: Limits{
			CookieDuration:   7_776_000,
			MaxFeeds:         131072,
			MaxCategories:    16384,
			MaxRegistrations: 1,
		},
	}
}

// System returns the installation-wide settings: what is stored over the
// defaults.
func (s *Store) System(ctx context.Context) (System, error) {
	system := DefaultSystem()
	stored, err := s.Setting(ctx, SettingSystem)
	if errors.Is(err, ErrNotFound) {
		return system, nil
	}
	if err != nil {
		return System{}, err
	}
	if err := json.Unmarshal([]byte(stored), &system); err != nil {
		return System{}, fmt.Errorf("store: setting %q: %w", SettingSystem, err)
	}
	return system, nil
}

// SetSystem replaces the installation-wide settings.
func (s *Store) SetSystem(ctx context.Context, system System) error {
	value, err := json.Marshal(system)
	if err != nil {
		return fmt.Errorf("store: set setting %q: %w", SettingSystem, err)
	}
	return s.SetSetting(ctx, SettingSystem, string(value))
}
