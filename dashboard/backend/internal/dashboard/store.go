package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"baize/shared/model"
)

type Release struct {
	ID         string    `json:"id"`
	Version    string    `json:"version"`
	OS         string    `json:"os"`
	Arch       string    `json:"arch"`
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
	UploadedAt time.Time `json:"uploaded_at"`
	Filename   string    `json:"-"`
}

type RobotRecord struct {
	UUID           string          `json:"uuid"`
	Code           string          `json:"code"`
	Model          string          `json:"model"`
	Hostname       string          `json:"hostname"`
	OS             string          `json:"os"`
	Arch           string          `json:"arch"`
	AgentVersion   string          `json:"agent_version"`
	Remark         string          `json:"remark"`
	DesiredVersion string          `json:"desired_version,omitempty"`
	LastSeen       time.Time       `json:"last_seen"`
	Telemetry      model.Telemetry `json:"telemetry"`
}

type persistentState struct {
	Remarks  map[string]string  `json:"remarks"`
	Desired  map[string]string  `json:"desired"`
	Releases map[string]Release `json:"releases"`
}

type Store struct {
	mu      sync.RWMutex
	dataDir string
	robots  map[string]RobotRecord
	state   persistentState
}

func NewStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, "releases"), 0o750); err != nil {
		return nil, err
	}
	store := &Store{
		dataDir: dataDir,
		robots:  make(map[string]RobotRecord),
		state: persistentState{
			Remarks:  make(map[string]string),
			Desired:  make(map[string]string),
			Releases: make(map[string]Release),
		},
	}
	if data, err := os.ReadFile(store.statePath()); err == nil {
		if err := json.Unmarshal(data, &store.state); err != nil {
			return nil, fmt.Errorf("read dashboard state: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if store.state.Remarks == nil {
		store.state.Remarks = make(map[string]string)
	}
	if store.state.Desired == nil {
		store.state.Desired = make(map[string]string)
	}
	if store.state.Releases == nil {
		store.state.Releases = make(map[string]Release)
	}
	for id, release := range store.state.Releases {
		release.Filename = filepath.Join(dataDir, "releases", id)
		if _, err := os.Stat(release.Filename); err != nil {
			delete(store.state.Releases, id)
			continue
		}
		store.state.Releases[id] = release
	}
	return store, nil
}

func (s *Store) PutTelemetry(telemetry model.Telemetry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	uuid := telemetry.Robot.UUID
	s.robots[uuid] = RobotRecord{
		UUID:           uuid,
		Code:           telemetry.Robot.Code,
		Model:          telemetry.Robot.Model,
		Hostname:       telemetry.Robot.Hostname,
		OS:             telemetry.Robot.OS,
		Arch:           telemetry.Robot.Arch,
		AgentVersion:   telemetry.AgentVersion,
		Remark:         s.state.Remarks[uuid],
		DesiredVersion: s.state.Desired[uuid],
		LastSeen:       time.Now().UTC(),
		Telemetry:      telemetry,
	}
}

func (s *Store) Robots() []RobotRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]RobotRecord, 0, len(s.robots))
	for _, robot := range s.robots {
		result = append(result, robot)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Code == result[j].Code {
			return result[i].UUID < result[j].UUID
		}
		return result[i].Code < result[j].Code
	})
	return result
}

func (s *Store) Robot(uuid string) (RobotRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.robots[uuid]
	return record, ok
}

func (s *Store) SetRemark(uuid, remark string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Remarks[uuid] = remark
	if robot, ok := s.robots[uuid]; ok {
		robot.Remark = remark
		s.robots[uuid] = robot
	}
	return s.saveLocked()
}

func (s *Store) SetDesired(uuid, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if version == "" {
		delete(s.state.Desired, uuid)
	} else {
		s.state.Desired[uuid] = version
	}
	if robot, ok := s.robots[uuid]; ok {
		robot.DesiredVersion = version
		s.robots[uuid] = robot
	}
	return s.saveLocked()
}

func (s *Store) AddRelease(release Release) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Releases[release.ID] = release
	return s.saveLocked()
}

func (s *Store) DeleteRelease(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, ok := s.state.Releases[id]
	if !ok {
		return os.ErrNotExist
	}
	for _, desired := range s.state.Desired {
		if desired == release.Version {
			return errors.New("release is assigned to a robot")
		}
	}
	if err := os.Remove(release.Filename); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	delete(s.state.Releases, id)
	return s.saveLocked()
}

func (s *Store) Releases() []Release {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Release, 0, len(s.state.Releases))
	for _, release := range s.state.Releases {
		result = append(result, release)
	}
	sort.Slice(result, func(i, j int) bool {
		comparison := compareVersions(result[i].Version, result[j].Version)
		if comparison == 0 {
			return result[i].UploadedAt.After(result[j].UploadedAt)
		}
		return comparison > 0
	})
	return result
}

func (s *Store) ReleaseByID(id string) (Release, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	release, ok := s.state.Releases[id]
	return release, ok
}

func (s *Store) FindUpdate(uuid, current, goos, arch string, automatic bool) (Release, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if desired := s.state.Desired[uuid]; desired != "" && desired != current {
		for _, release := range s.state.Releases {
			if release.Version == desired && release.OS == goos && release.Arch == arch {
				return release, true
			}
		}
	}
	if !automatic {
		return Release{}, false
	}
	var best Release
	found := false
	for _, release := range s.state.Releases {
		if release.OS != goos || release.Arch != arch || compareVersions(release.Version, current) <= 0 {
			continue
		}
		if !found || compareVersions(release.Version, best.Version) > 0 {
			best, found = release, true
		}
	}
	return best, found
}

func (s *Store) statePath() string { return filepath.Join(s.dataDir, "state.json") }

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(s.dataDir, ".state-*.json")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.statePath())
}

func compareVersions(left, right string) int {
	leftParts := versionNumbers(left)
	rightParts := versionNumbers(right)
	length := len(leftParts)
	if len(rightParts) > length {
		length = len(rightParts)
	}
	for i := 0; i < length; i++ {
		var l, r int
		if i < len(leftParts) {
			l = leftParts[i]
		}
		if i < len(rightParts) {
			r = rightParts[i]
		}
		if l < r {
			return -1
		}
		if l > r {
			return 1
		}
	}
	return strings.Compare(left, right)
}

func versionNumbers(version string) []int {
	version = strings.TrimPrefix(strings.TrimPrefix(version, "v"), "V")
	if index := strings.IndexAny(version, "-+"); index >= 0 {
		version = version[:index]
	}
	parts := strings.Split(version, ".")
	result := make([]int, len(parts))
	for i, part := range parts {
		result[i], _ = strconv.Atoi(part)
	}
	return result
}
