package spicedb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/kirsle/configdir"
)

const identityFilePerms = 0600

type Identity struct {
	UserID string `json:"user_id"`
}

func identityPath() string {
	return filepath.Join(configdir.LocalConfig("gotoni"), "identity.json")
}

// LoadIdentity reads the local identity file. Returns nil if it doesn't exist.
func LoadIdentity() *Identity {
	data, err := os.ReadFile(identityPath())
	if err != nil {
		return nil
	}
	var id Identity
	if json.Unmarshal(data, &id) != nil || id.UserID == "" {
		return nil
	}
	return &id
}

// SaveIdentity writes an identity to the local config file.
func SaveIdentity(id *Identity) error {
	dir := filepath.Dir(identityPath())
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return fmt.Errorf("json.Marshal: %w", err)
	}
	if err := os.WriteFile(identityPath(), data, 0600); err != nil {
		return fmt.Errorf("write %s: %w", identityPath(), err)
	}
	return nil
}

// GenerateIdentity creates a new identity with a random UUID and saves it.
func GenerateIdentity() (*Identity, error) {
	id := &Identity{UserID: uuid.New().String()}
	if err := SaveIdentity(id); err != nil {
		return nil, err
	}
	return id, nil
}

// ResolveUserID returns the current user ID from the local identity file.
// Returns empty string if no identity exists yet.
func ResolveUserID() string {
	if id := LoadIdentity(); id != nil {
		return id.UserID
	}
	return ""
}

// --- Contacts ---------------------------------------------------------------

// Contacts maps nicknames to UUIDs. Stored at ~/.config/gotoni/contacts.json.
type Contacts map[string]string

func contactsPath() string {
	return filepath.Join(configdir.LocalConfig("gotoni"), "contacts.json")
}

func LoadContacts() Contacts {
	data, err := os.ReadFile(contactsPath())
	if err != nil {
		return Contacts{}
	}
	var c Contacts
	if json.Unmarshal(data, &c) != nil {
		return Contacts{}
	}
	return c
}

func SaveContacts(c Contacts) error {
	dir := filepath.Dir(contactsPath())
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("json.Marshal: %w", err)
	}
	return os.WriteFile(contactsPath(), data, identityFilePerms)
}

// SetContact saves a nickname -> UUID mapping.
func SetContact(nickname, userID string) error {
	c := LoadContacts()
	c[nickname] = userID
	return SaveContacts(c)
}

// ResolveContact returns the UUID for a nickname. If the input is already a
// valid UUID, it is returned as-is.
func ResolveContact(nameOrID string) string {
	if isUUID(nameOrID) {
		return nameOrID
	}
	c := LoadContacts()
	if id, ok := c[nameOrID]; ok {
		return id
	}
	return nameOrID
}

// NicknameForUUID returns the local nickname for a UUID, or empty string.
func NicknameForUUID(userID string) string {
	for name, id := range LoadContacts() {
		if id == userID {
			return name
		}
	}
	return ""
}

func isUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}
