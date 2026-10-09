// Package store 用 bbolt 持久化升级服务的元数据。
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	bolt "go.etcd.io/bbolt"

	"syntropy_update/internal/model"
)

var (
	bucketComponents = []byte("components")
	bucketReleases   = []byte("releases")
	bucketArtifacts  = []byte("artifacts")
	bucketChannels   = []byte("channels")
	bucketAudit      = []byte("audit")

	bucketDistributions = []byte("distributions")
	bucketPolicies      = []byte("policies")
	bucketInventories   = []byte("inventories")
	bucketPlans         = []byte("plans")
	bucketIgnores       = []byte("ignores")
	bucketResults       = []byte("results")

	// ErrNotFound 表示记录不存在。
	ErrNotFound = errors.New("store: not found")
)

// Store 是 bbolt 元数据存储。
type Store struct {
	db *bolt.DB
}

// Open 打开（或创建）数据库。
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("store: create dir: %w", err)
	}
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	store := &Store{db: db}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, bucket := range [][]byte{bucketComponents, bucketReleases, bucketArtifacts, bucketChannels, bucketAudit, bucketDistributions, bucketPolicies, bucketInventories, bucketPlans, bucketIgnores, bucketResults} {
			if _, err := tx.CreateBucketIfNotExists(bucket); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: init buckets: %w", err)
	}
	return store, nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

func putJSON(bucket *bolt.Bucket, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("store: marshal: %w", err)
	}
	return bucket.Put([]byte(key), encoded)
}

func getJSON(bucket *bolt.Bucket, key string, target any) error {
	raw := bucket.Get([]byte(key))
	if raw == nil {
		return ErrNotFound
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("store: unmarshal: %w", err)
	}
	return nil
}

func listJSON[T any](bucket *bolt.Bucket) ([]T, error) {
	items := make([]T, 0)
	if err := bucket.ForEach(func(_, value []byte) error {
		var item T
		if err := json.Unmarshal(value, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("store: list: %w", err)
	}
	return items, nil
}

// PutComponent 写入组件定义。
func (s *Store) PutComponent(component model.Component) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketComponents), component.Name, component)
	})
}

// GetComponent 读取组件定义。
func (s *Store) GetComponent(name string) (model.Component, error) {
	var component model.Component
	err := s.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx.Bucket(bucketComponents), name, &component)
	})
	return component, err
}

// ListComponents 列出全部组件。
func (s *Store) ListComponents() ([]model.Component, error) {
	var components []model.Component
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		components, err = listJSON[model.Component](tx.Bucket(bucketComponents))
		return err
	})
	return components, err
}

// PutRelease 写入发布记录。
func (s *Store) PutRelease(release model.Release) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketReleases), release.ID, release)
	})
}

// GetRelease 读取发布记录。
func (s *Store) GetRelease(id string) (model.Release, error) {
	var release model.Release
	err := s.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx.Bucket(bucketReleases), id, &release)
	})
	return release, err
}

// ListReleases 列出发布记录；component 为空时返回全部。
func (s *Store) ListReleases(component string) ([]model.Release, error) {
	var releases []model.Release
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		releases, err = listJSON[model.Release](tx.Bucket(bucketReleases))
		return err
	})
	if err != nil {
		return nil, err
	}
	if component == "" {
		return releases, nil
	}
	filtered := make([]model.Release, 0, len(releases))
	for _, release := range releases {
		if release.Component == component {
			filtered = append(filtered, release)
		}
	}
	return filtered, nil
}

// FindRelease 按 (component, channel, version) 查找发布。
func (s *Store) FindRelease(component, channel, version string) (model.Release, error) {
	releases, err := s.ListReleases(component)
	if err != nil {
		return model.Release{}, err
	}
	for _, release := range releases {
		if release.Channel == channel && release.Version == version {
			return release, nil
		}
	}
	return model.Release{}, ErrNotFound
}

func artifactKey(releaseID, slot, platformOS, arch string) string {
	return releaseID + "/" + slot + "/" + platformOS + "/" + arch
}

// PutArtifact 写入制品元数据。
func (s *Store) PutArtifact(artifact model.Artifact) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketArtifacts), artifactKey(artifact.ReleaseID, artifact.Slot, artifact.OS, artifact.Arch), artifact)
	})
}

// ListArtifacts 列出某次发布的全部制品。
func (s *Store) ListArtifacts(releaseID string) ([]model.Artifact, error) {
	var artifacts []model.Artifact
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		artifacts, err = listJSON[model.Artifact](tx.Bucket(bucketArtifacts))
		return err
	})
	if err != nil {
		return nil, err
	}
	filtered := make([]model.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.ReleaseID == releaseID {
			filtered = append(filtered, artifact)
		}
	}
	return filtered, nil
}

// SetChannelPointer 设置 (component, channel) 当前指向的发布。
func (s *Store) SetChannelPointer(pointer model.ChannelPointer) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketChannels), pointer.Component+"/"+pointer.Channel, pointer)
	})
}

// GetChannelPointer 读取渠道指针。
func (s *Store) GetChannelPointer(component, channel string) (model.ChannelPointer, error) {
	var pointer model.ChannelPointer
	err := s.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx.Bucket(bucketChannels), component+"/"+channel, &pointer)
	})
	return pointer, err
}

// ListChannelPointers 列出渠道指针；component 为空时返回全部。
func (s *Store) ListChannelPointers(component string) ([]model.ChannelPointer, error) {
	var pointers []model.ChannelPointer
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		pointers, err = listJSON[model.ChannelPointer](tx.Bucket(bucketChannels))
		return err
	})
	if err != nil {
		return nil, err
	}
	if component == "" {
		return pointers, nil
	}
	filtered := make([]model.ChannelPointer, 0, len(pointers))
	for _, pointer := range pointers {
		if pointer.Component == component {
			filtered = append(filtered, pointer)
		}
	}
	return filtered, nil
}

func distributionKey(channel, productVersion string) string {
	return channel + "/" + productVersion
}

// PutDistribution 写入分发定义。
func (s *Store) PutDistribution(distribution model.Distribution) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketDistributions), distributionKey(distribution.Channel, distribution.ProductVersion), distribution)
	})
}

// GetDistribution 读取分发定义。
func (s *Store) GetDistribution(channel, productVersion string) (model.Distribution, error) {
	var distribution model.Distribution
	err := s.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx.Bucket(bucketDistributions), distributionKey(channel, productVersion), &distribution)
	})
	return distribution, err
}

// ListDistributions 列出分发定义；channel 为空时返回全部。
func (s *Store) ListDistributions(channel string) ([]model.Distribution, error) {
	var distributions []model.Distribution
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		distributions, err = listJSON[model.Distribution](tx.Bucket(bucketDistributions))
		return err
	})
	if err != nil {
		return nil, err
	}
	if channel == "" {
		return distributions, nil
	}
	filtered := make([]model.Distribution, 0, len(distributions))
	for _, distribution := range distributions {
		if distribution.Channel == channel {
			filtered = append(filtered, distribution)
		}
	}
	return filtered, nil
}

// PutPolicy 写入组件分发策略。
func (s *Store) PutPolicy(policyValue model.Policy) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketPolicies), policyValue.Component+"/"+policyValue.Channel, policyValue)
	})
}

// GetPolicy 读取策略；不存在时返回 ErrNotFound。
func (s *Store) GetPolicy(component, channel string) (model.Policy, error) {
	var policyValue model.Policy
	err := s.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx.Bucket(bucketPolicies), component+"/"+channel, &policyValue)
	})
	return policyValue, err
}

// ListPolicies 列出策略；channel 为空时返回全部。
func (s *Store) ListPolicies(channel string) ([]model.Policy, error) {
	var policies []model.Policy
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		policies, err = listJSON[model.Policy](tx.Bucket(bucketPolicies))
		return err
	})
	if err != nil {
		return nil, err
	}
	if channel == "" {
		return policies, nil
	}
	filtered := make([]model.Policy, 0, len(policies))
	for _, policyValue := range policies {
		if policyValue.Channel == channel {
			filtered = append(filtered, policyValue)
		}
	}
	return filtered, nil
}

// PutInventory 按目标 ID 覆盖写入库存。
func (s *Store) PutInventory(target model.TargetInventory) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketInventories), target.ID, target)
	})
}

// ListInventory 列出全部库存快照。
func (s *Store) ListInventory() ([]model.TargetInventory, error) {
	var targets []model.TargetInventory
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		targets, err = listJSON[model.TargetInventory](tx.Bucket(bucketInventories))
		return err
	})
	return targets, err
}

// PutPlan 写入升级计划。
func (s *Store) PutPlan(plan model.Plan) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketPlans), plan.ID, plan)
	})
}

// GetPlan 读取升级计划。
func (s *Store) GetPlan(id string) (model.Plan, error) {
	var plan model.Plan
	err := s.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx.Bucket(bucketPlans), id, &plan)
	})
	return plan, err
}

// ListPlans 列出升级计划，按创建时间倒序。
func (s *Store) ListPlans(limit int) ([]model.Plan, error) {
	plans := make([]model.Plan, 0)
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		plans, err = listJSON[model.Plan](tx.Bucket(bucketPlans))
		return err
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].CreatedAtMS > plans[j].CreatedAtMS })
	if limit > 0 && len(plans) > limit {
		plans = plans[:limit]
	}
	return plans, nil
}

// PutIgnore 按 (目标, 组件) 覆盖写入忽略记录。
func (s *Store) PutIgnore(ignore model.Ignore) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketIgnores), ignore.TargetID+"/"+ignore.Component, ignore)
	})
}

// GetIgnore 读取忽略记录；不存在时返回 ErrNotFound。
func (s *Store) GetIgnore(targetID, component string) (model.Ignore, error) {
	var ignore model.Ignore
	err := s.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx.Bucket(bucketIgnores), targetID+"/"+component, &ignore)
	})
	return ignore, err
}

// ListIgnores 列出忽略记录，按 (目标, 组件) 排序；target 为空时返回全部。
func (s *Store) ListIgnores(target string) ([]model.Ignore, error) {
	var ignores []model.Ignore
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		ignores, err = listJSON[model.Ignore](tx.Bucket(bucketIgnores))
		return err
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(ignores, func(i, j int) bool {
		if ignores[i].TargetID != ignores[j].TargetID {
			return ignores[i].TargetID < ignores[j].TargetID
		}
		return ignores[i].Component < ignores[j].Component
	})
	if target == "" {
		return ignores, nil
	}
	filtered := make([]model.Ignore, 0, len(ignores))
	for _, entry := range ignores {
		if entry.TargetID == target {
			filtered = append(filtered, entry)
		}
	}
	return filtered, nil
}

// DeleteIgnore 删除忽略记录；不存在时返回 ErrNotFound。
func (s *Store) DeleteIgnore(targetID, component string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketIgnores)
		key := []byte(targetID + "/" + component)
		if bucket.Get(key) == nil {
			return ErrNotFound
		}
		return bucket.Delete(key)
	})
}

// PutResult 追加一条升级结果；ID 与时间戳没给时由这里补齐。
func (s *Store) PutResult(result model.UpgradeResult) error {
	if result.ID == "" {
		result.ID = model.NewID("result")
	}
	if result.AtMS == 0 {
		result.AtMS = model.NowMS()
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketResults), result.ID, result)
	})
}

// ListResults 列出升级结果，按时间倒序；target 为空时返回全部，limit <= 0 表示不限。
func (s *Store) ListResults(target string, limit int) ([]model.UpgradeResult, error) {
	var results []model.UpgradeResult
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		results, err = listJSON[model.UpgradeResult](tx.Bucket(bucketResults))
		return err
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].AtMS != results[j].AtMS {
			return results[i].AtMS > results[j].AtMS
		}
		return results[i].ID > results[j].ID
	})
	if target != "" {
		filtered := make([]model.UpgradeResult, 0, len(results))
		for _, entry := range results {
			if entry.TargetID == target {
				filtered = append(filtered, entry)
			}
		}
		results = filtered
	}
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// AppendAudit 追加一条审计记录。
func (s *Store) AppendAudit(entry model.AuditEntry) error {
	if entry.ID == "" {
		entry.ID = model.NewID("audit")
	}
	if entry.AtMS == 0 {
		entry.AtMS = model.NowMS()
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("store: marshal audit: %w", err)
	}
	key := fmt.Sprintf("%013d-%s", entry.AtMS, entry.ID)
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketAudit).Put([]byte(key), encoded)
	})
}

// ListAudit 返回最近的审计记录（按时间倒序）。
func (s *Store) ListAudit(limit int) ([]model.AuditEntry, error) {
	entries := make([]model.AuditEntry, 0)
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketAudit).Cursor()
		for key, value := cursor.Last(); key != nil && (limit <= 0 || len(entries) < limit); key, value = cursor.Prev() {
			var entry model.AuditEntry
			if err := json.Unmarshal(value, &entry); err != nil {
				return err
			}
			entries = append(entries, entry)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	return entries, nil
}
