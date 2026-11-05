package sitechannels

import (
	"context"
	"errors"
	"net/mail"
	"strings"

	"github.com/andrometiq/relay_hub/internal/channels"
	"github.com/andrometiq/relay_hub/internal/containers"
	relayemail "github.com/andrometiq/relay_hub/internal/email"
)

var (
	missingMailbox = Missing{
		NotFound: &Failure{"not_found", "This mailbox is not connected to this site."},
		None:     &Failure{"no_mailbox", "This site has no mailbox."},
		Choose:   &Failure{"channel_required", "This site has several mailboxes; choose one."},
	}
	errHostRefused = &Failure{"host_refused", "Relay can only reach mail servers on the public internet. Check the host names."}
	errPortRefused = &Failure{"port_refused", "Use SMTP port 25, 465, 587 or 2525 and IMAP port 143 or 993."}
)

// serverFields are the public mail server settings a site may set.
var serverFields = []string{"host", "port", "username", "imap_host", "imap_port", "imap_username", "imap_folder", "trusted_authserv_id", "trusted_dsn_domain"}

// MailboxServer is how the Hub signs in to a mailbox. Blank fields keep the
// stored value on a change; passwords are write-only.
type MailboxServer struct {
	Host              string `json:"host,omitempty"`
	Port              int    `json:"port,omitempty"`
	Username          string `json:"username,omitempty"`
	Password          string `json:"password,omitempty"`
	IMAPHost          string `json:"imap_host,omitempty"`
	IMAPPort          int    `json:"imap_port,omitempty"`
	IMAPUsername      string `json:"imap_username,omitempty"`
	IMAPPassword      string `json:"imap_password,omitempty"`
	IMAPFolder        string `json:"imap_folder,omitempty"`
	TrustedAuthservID string `json:"trusted_authserv_id,omitempty"`
	TrustedDSNDomain  string `json:"trusted_dsn_domain,omitempty"`
}

// apply writes the set fields into config and returns the new SMTP password, if any.
func (m MailboxServer) apply(config map[string]any) string {
	for name, value := range map[string]string{"host": m.Host, "username": m.Username, "imap_host": m.IMAPHost, "imap_username": m.IMAPUsername,
		"imap_password": m.IMAPPassword, "imap_folder": m.IMAPFolder, "trusted_authserv_id": m.TrustedAuthservID, "trusted_dsn_domain": m.TrustedDSNDomain} {
		if value = strings.TrimSpace(value); value != "" {
			config[name] = value
		}
	}
	for name, value := range map[string]int{"port": m.Port, "imap_port": m.IMAPPort} {
		if value != 0 {
			config[name] = value
		}
	}
	return m.Password
}

// MailboxInput connects a mailbox. From is its address and identity.
type MailboxInput struct {
	Label string `json:"label,omitempty"`
	From  string `json:"from"`
	MailboxServer
}

// MailboxSettings changes one mailbox. Nil fields and blank server fields keep their values.
type MailboxSettings struct {
	Channel string  `json:"channel,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
	Label   *string `json:"label,omitempty"`
	MailboxServer
}

// MailboxHealth is what a site may see about one of its mailboxes. Passwords show only as saved or not.
type MailboxHealth struct {
	Channel           string                   `json:"channel"`
	Label             string                   `json:"label"`
	Address           string                   `json:"address"`
	Enabled           bool                     `json:"enabled"`
	AdminOff          bool                     `json:"admin_off"`
	ConnectionStatus  string                   `json:"connection_status"`
	SMTPStatus        string                   `json:"smtp_status"`
	IMAPStatus        string                   `json:"imap_status"`
	CheckedAt         string                   `json:"checked_at"`
	CheckError        string                   `json:"check_error"`
	PasswordSaved     bool                     `json:"password_saved"`
	IMAPPasswordSaved bool                     `json:"imap_password_saved"`
	Server            map[string]any           `json:"server"`
	Sync              []channels.MailboxHealth `json:"sync"`
	Rejected          int                      `json:"rejected"`
}

// MailboxRemoval says what happened when a mailbox left the site.
type MailboxRemoval struct {
	Channel string `json:"channel"`
	Removed bool   `json:"removed"`
}

// tryMailbox checks settings and signs in to SMTP and IMAP. A failure is a
// coded refusal; nothing is stored before this passes.
func (s *Service) tryMailbox(ctx context.Context, key string, config map[string]any, password string) (map[string]any, error) {
	if err := channels.ValidateEmailSettings(config, password); err != nil {
		return nil, &Failure{"invalid_settings", "Check the mailbox settings: " + err.Error() + "."}
	}
	cfg := channels.EmailConfig(key, config, password)
	imapPort := cfg.IMAPPort
	if cfg.IMAPHost != "" && imapPort == 0 {
		imapPort = 993
	}
	if relayemail.CheckPorts(cfg.Port, imapPort) != nil {
		return nil, errPortRefused
	}
	for _, host := range []string{cfg.Host, cfg.IMAPHost} {
		if _, err := s.Hosts.Resolve(ctx, host); host != "" && errors.Is(err, relayemail.ErrHostRefused) {
			return nil, errHostRefused
		} else if host != "" && err != nil {
			return nil, &Failure{"host_unknown", "Relay could not find the mail server " + host + ". Check the host name."}
		}
	}
	client, err := s.Clients.Email(cfg)
	if err != nil {
		return nil, &Failure{"invalid_settings", "Check the mailbox settings."}
	}
	result := channels.TestEmail(ctx, client)
	if result.Status() == "failed" {
		return nil, &Failure{"login_failed", result.Failure()}
	}
	return map[string]any{"connection_status": "connected", "smtp_status": result.SMTP, "imap_status": result.IMAP, "checked_at": s.now(), "check_error": ""}, nil
}

// ConnectMailbox tests SMTP and IMAP sign-in, then stores the mailbox for
// site and links it. The IMAP reader starts at new mail after the reload.
func (s *Service) ConnectMailbox(ctx context.Context, site string, in MailboxInput) (MailboxHealth, error) {
	ref := containers.ChannelRef{Channel: "Email"}
	health, err := s.connectMailbox(ctx, site, in, &ref)
	s.audit(ctx, site, ref, "email.mailbox.connect", err)
	return health, err
}

func (s *Service) connectMailbox(ctx context.Context, site string, in MailboxInput, ref *containers.ChannelRef) (MailboxHealth, error) {
	if err := s.allow(ctx, site, "Email", "email.mailbox.connect", connectLimit); err != nil {
		return MailboxHealth{}, err
	}
	from, err := mail.ParseAddress(strings.TrimSpace(in.From))
	if err != nil || from.Name != "" {
		return MailboxHealth{}, &Failure{"invalid_settings", "Enter the mailbox address, such as sales@example.com."}
	}
	config := map[string]any{"from": strings.ToLower(from.Address)}
	if in.Username == "" {
		in.Username = from.Address
	}
	password := in.apply(config)
	status, err := s.tryMailbox(ctx, "connect", config, password)
	if err != nil {
		return MailboxHealth{}, err
	}
	for name, value := range status {
		config[name] = value
	}
	label := strings.TrimSpace(in.Label)
	if label == "" {
		label = from.Address
	}
	if ref.Key, err = s.Store.SaveSiteChannel(ctx, site, containers.Channel{Channel: "Email", Label: label, Config: config}, password); err != nil {
		return MailboxHealth{}, stored(err)
	}
	s.reload(ctx, site, ref.Key)
	return s.MailboxHealth(ctx, site, ref.Key)
}

// MailboxHealth reads the stored state and the sync state of one mailbox. It never contacts a mail server.
func (s *Service) MailboxHealth(ctx context.Context, site, key string) (MailboxHealth, error) {
	c, err := Channel(ctx, s.Store, "Email", site, key, missingMailbox)
	if err != nil {
		return MailboxHealth{}, err
	}
	return s.MailboxHealthOf(ctx, c)
}

// MailboxHealthOf is the health of a mailbox that the caller already read for its site.
func (s *Service) MailboxHealthOf(ctx context.Context, c containers.Channel) (MailboxHealth, error) {
	sync, err := channels.MailboxStatus(ctx, s.Store.DB, c.Key)
	if err != nil {
		return MailboxHealth{}, ErrUnavailable
	}
	health := MailboxHealth{Channel: c.Key, Label: c.Label, Address: c.Address, Enabled: c.Enabled, AdminOff: c.AdminOff, ConnectionStatus: text(c.Config, "connection_status"),
		SMTPStatus: text(c.Config, "smtp_status"), IMAPStatus: text(c.Config, "imap_status"), CheckedAt: text(c.Config, "checked_at"),
		CheckError: text(c.Config, "check_error"), PasswordSaved: saved(c, "secret"), IMAPPasswordSaved: saved(c, "imap_password"),
		Server: map[string]any{}, Sync: sync}
	for _, name := range serverFields {
		if value, ok := c.Config[name]; ok {
			health.Server[name] = value
		}
	}
	if err = s.Store.DB.QueryRow(ctx, `SELECT count(*) FROM relay_mailbox_rejections WHERE account=$1`, c.Key).Scan(&health.Rejected); err != nil {
		return MailboxHealth{}, ErrUnavailable
	}
	return health, nil
}

// CheckMailbox signs in to SMTP and IMAP with the mailbox the runtime uses and records the result.
func (s *Service) CheckMailbox(ctx context.Context, site, key string) (MailboxHealth, error) {
	c, err := Channel(ctx, s.Store, "Email", site, key, missingMailbox)
	if err != nil {
		return MailboxHealth{}, err
	}
	if !c.Enabled {
		return MailboxHealth{}, errDisabled
	}
	if err = s.allow(ctx, site, "Email", "email.mailbox.check", checkLimit); err != nil {
		return MailboxHealth{}, err
	}
	values := map[string]any{"connection_status": "failed", "checked_at": s.now(), "check_error": "The mailbox is not active in Relay Hub yet. Try again in a minute."}
	if result, err := s.Registry.TestEmailConnection(ctx, c.Key); err == nil {
		values["connection_status"], values["smtp_status"], values["imap_status"], values["check_error"] = result.Status(), result.SMTP, result.IMAP, ""
		if result.Status() == "failed" {
			values["check_error"] = result.Failure()
		}
	}
	if err = s.Store.UpdateSiteValues(ctx, site, containers.ChannelRef{Channel: "Email", Key: c.Key}, nil, values); err != nil {
		return MailboxHealth{}, ErrUnavailable
	}
	return s.MailboxHealth(ctx, site, c.Key)
}

// SaveMailboxSettings renames a mailbox, turns it on or off, or changes how
// the Hub signs in. New sign-in settings are tested before they are stored;
// a new host, user or folder makes the reader start again at new mail.
func (s *Service) SaveMailboxSettings(ctx context.Context, site string, in MailboxSettings) (MailboxHealth, error) {
	if in.Enabled == nil && in.Label == nil && in.MailboxServer == (MailboxServer{}) {
		return MailboxHealth{}, errNoChange
	}
	c, err := Channel(ctx, s.Store, "Email", site, in.Channel, missingMailbox)
	if err != nil {
		return MailboxHealth{}, err
	}
	health, err := s.saveMailboxSettings(ctx, site, c, in)
	s.audit(ctx, site, containers.ChannelRef{Channel: "Email", Key: c.Key}, "email.mailbox.settings", err)
	return health, err
}

func (s *Service) saveMailboxSettings(ctx context.Context, site string, c containers.Channel, in MailboxSettings) (MailboxHealth, error) {
	password := ""
	if in.MailboxServer != (MailboxServer{}) {
		if err := s.allow(ctx, site, "Email", "email.mailbox.connect", connectLimit); err != nil {
			return MailboxHealth{}, err
		}
		credential, err := s.Store.Credential(ctx, "Email", c.Key)
		if err != nil {
			return MailboxHealth{}, ErrUnavailable
		}
		password = in.apply(credential.Config)
		if password == "" {
			password = credential.Secret
		}
		status, err := s.tryMailbox(ctx, c.Key, credential.Config, password)
		if err != nil {
			return MailboxHealth{}, err
		}
		in.apply(c.Config)
		for name, value := range status {
			c.Config[name] = value
		}
		c.Config["site_managed"] = true
		password = in.Password
	}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	if in.Label != nil && strings.TrimSpace(*in.Label) != "" {
		c.Label = strings.TrimSpace(*in.Label)
	}
	if err := s.Store.UpdateSiteChannel(ctx, site, c, password); err != nil {
		return MailboxHealth{}, stored(err)
	}
	if err := s.Reload(ctx); err != nil {
		return MailboxHealth{}, ErrUnavailable
	}
	return s.MailboxHealth(ctx, site, c.Key)
}

// RemoveMailbox takes a mailbox off the site after confirm. Both passwords
// are wiped with the unlink; the reader stops at the reload.
func (s *Service) RemoveMailbox(ctx context.Context, site, key string, confirm bool) (MailboxRemoval, error) {
	if !confirm {
		return MailboxRemoval{}, errConfirm
	}
	c, err := Channel(ctx, s.Store, "Email", site, key, missingMailbox)
	if err != nil {
		return MailboxRemoval{}, err
	}
	ref := containers.ChannelRef{Channel: "Email", Key: c.Key}
	err = stored(s.Store.RemoveSiteChannel(ctx, site, ref))
	s.audit(ctx, site, ref, "email.mailbox.remove", err)
	if err != nil {
		return MailboxRemoval{}, err
	}
	s.reload(ctx, site, c.Key)
	return MailboxRemoval{Channel: c.Key, Removed: true}, nil
}
