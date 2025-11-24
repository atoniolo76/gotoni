package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/glebarez/go-sqlite"
	"github.com/kirsle/configdir"
)

// DB wraps the sql.DB connection
type DB struct {
	*sql.DB
}

// Instance represents an instance record in the database
type Instance struct {
	ID             string
	Name           string
	Region         string
	DockerImage    string
	Status         string
	SSHKeyName     string
	FilesystemName string
	InstanceType   string
	IPAddress      string
	CreatedAt      time.Time
}

// SSHKey represents an SSH key record
type SSHKey struct {
	Name       string
	PrivateKey string // Path to private key file
}

// Filesystem represents a filesystem record
type Filesystem struct {
	Name   string
	ID     string
	Region string
}

// Task represents a task record
type Task struct {
	ID            int64
	Name          string
	Type          string
	Command       string
	Background    bool
	WorkingDir    string
	Env           string // JSON string
	DependsOn     string // JSON string
	WhenCondition string
	Restart       string
	RestartSec    int
}

// InitDB initializes the database connection and creates tables if they don't exist
func InitDB() (*DB, error) {
	configPath := configdir.LocalConfig("gotoni")
	if err := configdir.MakePath(configPath); err != nil {
		return nil, fmt.Errorf("failed to create config directory: %w", err)
	}

	dbPath := filepath.Join(configPath, "data.db")

	// Ensure the directory exists (redundant with MakePath but good for safety)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	database := &DB{db}
	if err := database.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return database, nil
}

func (d *DB) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS instances (
			id TEXT PRIMARY KEY,
			name TEXT,
			region TEXT,
			docker_image TEXT,
			status TEXT,
			ssh_key_name TEXT,
			filesystem_name TEXT,
			instance_type TEXT,
			ip_address TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS ssh_keys (
			name TEXT PRIMARY KEY,
			private_key TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS filesystems (
			name TEXT PRIMARY KEY,
			id TEXT,
			region TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT,
			type TEXT,
			command TEXT,
			background BOOLEAN,
			working_dir TEXT,
			env TEXT,
			depends_on TEXT,
			when_condition TEXT,
			restart TEXT,
			restart_sec INTEGER
		);`,
	}

	for _, query := range queries {
		if _, err := d.Exec(query); err != nil {
			return fmt.Errorf("failed to execute migration query: %s, error: %w", query, err)
		}
	}
	return nil
}

// SaveInstance upserts an instance into the database
func (d *DB) SaveInstance(inst *Instance) error {
	query := `
	INSERT INTO instances (id, name, region, docker_image, status, ssh_key_name, filesystem_name, instance_type, ip_address, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		region = excluded.region,
		docker_image = excluded.docker_image,
		status = excluded.status,
		ssh_key_name = excluded.ssh_key_name,
		filesystem_name = excluded.filesystem_name,
		instance_type = excluded.instance_type,
		ip_address = excluded.ip_address;
	`
	_, err := d.Exec(query, inst.ID, inst.Name, inst.Region, inst.DockerImage, inst.Status, inst.SSHKeyName, inst.FilesystemName, inst.InstanceType, inst.IPAddress, inst.CreatedAt)
	return err
}

// GetInstance retrieves an instance by ID
func (d *DB) GetInstance(id string) (*Instance, error) {
	query := `SELECT id, name, region, docker_image, status, ssh_key_name, filesystem_name, instance_type, ip_address, created_at FROM instances WHERE id = ?`
	row := d.QueryRow(query, id)

	var inst Instance
	err := row.Scan(&inst.ID, &inst.Name, &inst.Region, &inst.DockerImage, &inst.Status, &inst.SSHKeyName, &inst.FilesystemName, &inst.InstanceType, &inst.IPAddress, &inst.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &inst, nil
}

// GetInstanceByIP retrieves an instance by IP
func (d *DB) GetInstanceByIP(ip string) (*Instance, error) {
	query := `SELECT id, name, region, docker_image, status, ssh_key_name, filesystem_name, instance_type, ip_address, created_at FROM instances WHERE ip_address = ?`
	row := d.QueryRow(query, ip)

	var inst Instance
	err := row.Scan(&inst.ID, &inst.Name, &inst.Region, &inst.DockerImage, &inst.Status, &inst.SSHKeyName, &inst.FilesystemName, &inst.InstanceType, &inst.IPAddress, &inst.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &inst, nil
}

// ListInstances retrieves all instances
func (d *DB) ListInstances() ([]Instance, error) {
	query := `SELECT id, name, region, docker_image, status, ssh_key_name, filesystem_name, instance_type, ip_address, created_at FROM instances`
	rows, err := d.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var instances []Instance
	for rows.Next() {
		var inst Instance
		err := rows.Scan(&inst.ID, &inst.Name, &inst.Region, &inst.DockerImage, &inst.Status, &inst.SSHKeyName, &inst.FilesystemName, &inst.InstanceType, &inst.IPAddress, &inst.CreatedAt)
		if err != nil {
			return nil, err
		}
		instances = append(instances, inst)
	}
	return instances, nil
}

// DeleteInstance removes an instance from the database
func (d *DB) DeleteInstance(id string) error {
	_, err := d.Exec("DELETE FROM instances WHERE id = ?", id)
	return err
}

// UpdateInstanceStatus updates the status of an instance
func (d *DB) UpdateInstanceStatus(id, status string) error {
	_, err := d.Exec("UPDATE instances SET status = ? WHERE id = ?", status, id)
	return err
}

// UpdateInstanceIP updates the IP address of an instance
func (d *DB) UpdateInstanceIP(id, ip string) error {
	_, err := d.Exec("UPDATE instances SET ip_address = ? WHERE id = ?", ip, id)
	return err
}

// SaveSSHKey upserts an SSH key
func (d *DB) SaveSSHKey(key *SSHKey) error {
	query := `INSERT INTO ssh_keys (name, private_key) VALUES (?, ?) ON CONFLICT(name) DO UPDATE SET private_key = excluded.private_key`
	_, err := d.Exec(query, key.Name, key.PrivateKey)
	return err
}

// GetSSHKey retrieves an SSH key by name
func (d *DB) GetSSHKey(name string) (*SSHKey, error) {
	query := `SELECT name, private_key FROM ssh_keys WHERE name = ?`
	row := d.QueryRow(query, name)
	var key SSHKey
	err := row.Scan(&key.Name, &key.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &key, nil
}

// ListSSHKeys retrieves all SSH keys
func (d *DB) ListSSHKeys() ([]SSHKey, error) {
	query := `SELECT name, private_key FROM ssh_keys`
	rows, err := d.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []SSHKey
	for rows.Next() {
		var key SSHKey
		if err := rows.Scan(&key.Name, &key.PrivateKey); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// DeleteSSHKey deletes an SSH key by name
func (d *DB) DeleteSSHKey(name string) error {
	_, err := d.Exec("DELETE FROM ssh_keys WHERE name = ?", name)
	return err
}

// SaveFilesystem upserts a filesystem
func (d *DB) SaveFilesystem(fs *Filesystem) error {
	query := `INSERT INTO filesystems (name, id, region) VALUES (?, ?, ?) ON CONFLICT(name) DO UPDATE SET id = excluded.id, region = excluded.region`
	_, err := d.Exec(query, fs.Name, fs.ID, fs.Region)
	return err
}

// GetFilesystem retrieves a filesystem by name
func (d *DB) GetFilesystem(name string) (*Filesystem, error) {
	query := `SELECT name, id, region FROM filesystems WHERE name = ?`
	row := d.QueryRow(query, name)
	var fs Filesystem
	err := row.Scan(&fs.Name, &fs.ID, &fs.Region)
	if err != nil {
		return nil, err
	}
	return &fs, nil
}

// ListFilesystems retrieves all filesystems
func (d *DB) ListFilesystems() ([]Filesystem, error) {
	query := `SELECT name, id, region FROM filesystems`
	rows, err := d.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var filesystems []Filesystem
	for rows.Next() {
		var fs Filesystem
		if err := rows.Scan(&fs.Name, &fs.ID, &fs.Region); err != nil {
			return nil, err
		}
		filesystems = append(filesystems, fs)
	}
	return filesystems, nil
}

// SaveTask saves a task
func (d *DB) SaveTask(task *Task) error {
	query := `INSERT INTO tasks (name, type, command, background, working_dir, env, depends_on, when_condition, restart, restart_sec) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := d.Exec(query, task.Name, task.Type, task.Command, task.Background, task.WorkingDir, task.Env, task.DependsOn, task.WhenCondition, task.Restart, task.RestartSec)
	return err
}

// ListTasks retrieves all tasks
func (d *DB) ListTasks() ([]Task, error) {
	query := `SELECT id, name, type, command, background, working_dir, env, depends_on, when_condition, restart, restart_sec FROM tasks`
	rows, err := d.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Name, &t.Type, &t.Command, &t.Background, &t.WorkingDir, &t.Env, &t.DependsOn, &t.WhenCondition, &t.Restart, &t.RestartSec); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

// DeleteTask deletes a task by name
func (d *DB) DeleteTask(name string) error {
	_, err := d.Exec("DELETE FROM tasks WHERE name = ?", name)
	return err
}
