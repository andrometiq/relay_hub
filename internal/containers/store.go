// Package containers owns containers, their channels, sites and channel assignment.
// One store owns these tables because they share one encryption key and one transaction.
package containers

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var idRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
var channels = map[string]bool{"WhatsApp": true, "Telegram": true, "Email": true}

// ErrConflict means the request names a record owned by another container or site.
var ErrConflict = errors.New("record belongs to another container or site")

type Store struct {
	DB  *pgxpool.Pool
	Key []byte
}

func New(db *pgxpool.Pool, key string) (*Store, error) {
	h := sha256.Sum256([]byte(key))
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("RELAY_CREDENTIAL_KEY is required")
	}
	return &Store{DB: db, Key: h[:]}, nil
}

func (s *Store) box(secret string) ([]byte, []byte, error) {
	b, e := aes.NewCipher(s.Key)
	if e != nil {
		return nil, nil, e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, nil, e
	}
	n := make([]byte, g.NonceSize())
	if _, e = rand.Read(n); e != nil {
		return nil, nil, e
	}
	return g.Seal(nil, n, []byte(secret), nil), n, nil
}

func (s *Store) unbox(c, n []byte) (string, error) {
	b, e := aes.NewCipher(s.Key)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return "", e
	}
	if len(n) != g.NonceSize() {
		return "", errors.New("invalid credential nonce")
	}
	v, e := g.Open(nil, n, c, nil)
	return string(v), e
}

// boxOptional encrypts a non-empty value; an empty value stays empty.
func (s *Store) boxOptional(secret string) ([]byte, []byte, error) {
	if secret == "" {
		return []byte{}, []byte{}, nil
	}
	return s.box(secret)
}

func (s *Store) boxMap(values map[string]string) ([]byte, []byte, error) {
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, nil, errors.New("invalid credential configuration")
	}
	return s.box(string(raw))
}

func validID(id string) bool { return idRE.MatchString(id) }

func validOrigin(origin string) error {
	u, e := url.Parse(origin)
	if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || len(origin) > 300 || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("site must be an HTTP or HTTPS origin")
	}
	return nil
}

func validate(id, site string) error {
	if !validID(id) {
		return errors.New("invalid account id")
	}
	return validOrigin(site)
}

func likePattern(query string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(query)) + "%"
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
