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

// Cluster represents a cluster record
type Cluster struct {
	ID             int64
	Name           string
	SSHKeyName     string
	FilesystemName string
	Status         string
	CreatedAt      time.Time
}

// ClusterReplica represents a replica specification within a cluster
type ClusterReplica struct {
	ID           int64
	ClusterID    int64
	InstanceType string
	Region       string
	Quantity     int
	Name         string
}

// ClusterInstance represents an instance belonging to a cluster
type ClusterInstance struct {
	ID         int64
	ClusterID  int64
	InstanceID string // Reference to the instance ID in instances table
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
		`CREATE TABLE IF NOT EXISTS clusters (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE,
			ssh_key_name TEXT,
			filesystem_name TEXT,
			status TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS cluster_replicas (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			cluster_id INTEGER,
			instance_type TEXT,
			region TEXT,
			quantity INTEGER,
			name TEXT,
			FOREIGN KEY (cluster_id) REFERENCES clusters(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS cluster_instances (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			cluster_id INTEGER,
			instance_id TEXT,
			FOREIGN KEY (cluster_id) REFERENCES clusters(id) ON DELETE CASCADE,
			FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE
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

// SaveCluster saves a cluster to the database
func (d *DB) SaveCluster(cluster *Cluster) (int64, error) {
	query := `
	INSERT INTO clusters (name, ssh_key_name, filesystem_name, status, created_at)
	VALUES (?, ?, ?, ?, ?)
	ON CONFLICT(name) DO UPDATE SET
		ssh_key_name = excluded.ssh_key_name,
		filesystem_name = excluded.filesystem_name,
		status = excluded.status;
	`
	result, err := d.Exec(query, cluster.Name, cluster.SSHKeyName, cluster.FilesystemName, cluster.Status, cluster.CreatedAt)
	if err != nil {
		return 0, err
	}

	// If it was an update, get the existing ID
	if cluster.ID != 0 {
		return cluster.ID, nil
	}

	return result.LastInsertId()
}

// GetCluster retrieves a cluster by name
func (d *DB) GetCluster(name string) (*Cluster, error) {
	query := `SELECT id, name, COALESCE(ssh_key_name, ''), COALESCE(filesystem_name, ''), COALESCE(status, ''), created_at FROM clusters WHERE name = ?`
	row := d.QueryRow(query, name)

	var cluster Cluster
	err := row.Scan(&cluster.ID, &cluster.Name, &cluster.SSHKeyName, &cluster.FilesystemName, &cluster.Status, &cluster.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &cluster, nil
}

// GetClusterByID retrieves a cluster by ID
func (d *DB) GetClusterByID(id int64) (*Cluster, error) {
	query := `SELECT id, name, COALESCE(ssh_key_name, ''), COALESCE(filesystem_name, ''), COALESCE(status, ''), created_at FROM clusters WHERE id = ?`
	row := d.QueryRow(query, id)

	var cluster Cluster
	err := row.Scan(&cluster.ID, &cluster.Name, &cluster.SSHKeyName, &cluster.FilesystemName, &cluster.Status, &cluster.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &cluster, nil
}

// ListClusters retrieves all clusters
func (d *DB) ListClusters() ([]Cluster, error) {
	query := `SELECT id, name, COALESCE(ssh_key_name, ''), COALESCE(filesystem_name, ''), COALESCE(status, ''), created_at FROM clusters`
	rows, err := d.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var clusters []Cluster
	for rows.Next() {
		var cluster Cluster
		err := rows.Scan(&cluster.ID, &cluster.Name, &cluster.SSHKeyName, &cluster.FilesystemName, &cluster.Status, &cluster.CreatedAt)
		if err != nil {
			return nil, err
		}
		clusters = append(clusters, cluster)
	}
	return clusters, nil
}

// DeleteCluster deletes a cluster by name (cascades to replicas and instances)
func (d *DB) DeleteCluster(name string) error {
	// Get cluster ID first
	cluster, err := d.GetCluster(name)
	if err != nil {
		return err
	}

	// Delete cluster instances
	_, err = d.Exec("DELETE FROM cluster_instances WHERE cluster_id = ?", cluster.ID)
	if err != nil {
		return err
	}

	// Delete cluster replicas
	_, err = d.Exec("DELETE FROM cluster_replicas WHERE cluster_id = ?", cluster.ID)
	if err != nil {
		return err
	}

	// Delete cluster
	_, err = d.Exec("DELETE FROM clusters WHERE id = ?", cluster.ID)
	return err
}

// UpdateClusterStatus updates the status of a cluster
func (d *DB) UpdateClusterStatus(name, status string) error {
	_, err := d.Exec("UPDATE clusters SET status = ? WHERE name = ?", status, name)
	return err
}

// SaveClusterReplica saves a cluster replica specification
func (d *DB) SaveClusterReplica(replica *ClusterReplica) (int64, error) {
	query := `INSERT INTO cluster_replicas (cluster_id, instance_type, region, quantity, name) VALUES (?, ?, ?, ?, ?)`
	result, err := d.Exec(query, replica.ClusterID, replica.InstanceType, replica.Region, replica.Quantity, replica.Name)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetClusterReplicas retrieves all replicas for a cluster
func (d *DB) GetClusterReplicas(clusterID int64) ([]ClusterReplica, error) {
	query := `SELECT id, cluster_id, instance_type, region, quantity, name FROM cluster_replicas WHERE cluster_id = ?`
	rows, err := d.Query(query, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var replicas []ClusterReplica
	for rows.Next() {
		var replica ClusterReplica
		err := rows.Scan(&replica.ID, &replica.ClusterID, &replica.InstanceType, &replica.Region, &replica.Quantity, &replica.Name)
		if err != nil {
			return nil, err
		}
		replicas = append(replicas, replica)
	}
	return replicas, nil
}

// DeleteClusterReplicas deletes all replicas for a cluster
func (d *DB) DeleteClusterReplicas(clusterID int64) error {
	_, err := d.Exec("DELETE FROM cluster_replicas WHERE cluster_id = ?", clusterID)
	return err
}

// AddInstanceToCluster adds an instance to a cluster
func (d *DB) AddInstanceToCluster(clusterID int64, instanceID string) error {
	query := `INSERT INTO cluster_instances (cluster_id, instance_id) VALUES (?, ?)`
	_, err := d.Exec(query, clusterID, instanceID)
	return err
}

// GetClusterInstances retrieves all instance IDs for a cluster
func (d *DB) GetClusterInstances(clusterID int64) ([]string, error) {
	query := `SELECT instance_id FROM cluster_instances WHERE cluster_id = ?`
	rows, err := d.Query(query, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var instanceIDs []string
	for rows.Next() {
		var instanceID string
		err := rows.Scan(&instanceID)
		if err != nil {
			return nil, err
		}
		instanceIDs = append(instanceIDs, instanceID)
	}
	return instanceIDs, nil
}

// RemoveInstanceFromCluster removes an instance from a cluster
func (d *DB) RemoveInstanceFromCluster(clusterID int64, instanceID string) error {
	_, err := d.Exec("DELETE FROM cluster_instances WHERE cluster_id = ? AND instance_id = ?", clusterID, instanceID)
	return err
}

// ClearClusterInstances removes all instances from a cluster
func (d *DB) ClearClusterInstances(clusterID int64) error {
	_, err := d.Exec("DELETE FROM cluster_instances WHERE cluster_id = ?", clusterID)
	return err
}
