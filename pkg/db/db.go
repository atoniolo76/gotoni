package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kirsle/configdir"
	_ "github.com/mattn/go-sqlite3"
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

	db, err := sql.Open("sqlite3", dbPath)
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
	query := `
	CREATE TABLE IF NOT EXISTS instances (
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
	);
	`
	_, err := d.Exec(query)
	return err
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
