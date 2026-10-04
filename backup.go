package main

// 备份与恢复：写配置前把目标文件原样快照到数据目录，恢复时按快照还原（原本不存在的文件删掉）。
// 参考项目把快照打包成带校验和的 JSON 版本包；我们用"一目录一版本 + meta.json"的直白形态，效果等价、肉眼可查。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type backupFile struct {
	Path    string `json:"path"`
	Existed bool   `json:"existed"`
	Name    string `json:"name"`
	// After 是"写入后"的快照文件名（写入成功才填）：
	// 「恢复」用它把配置还原成工具写好的样子，「撤销」用 Name 回到写入之前。
	After string `json:"after,omitempty"`
}

type backupMeta struct {
	ID        string       `json:"id"`
	Client    string       `json:"client"`
	CreatedAt string       `json:"createdAt"`
	Base      string       `json:"base"`
	Model     string       `json:"model"`
	Files     []backupFile `json:"files"`
}

type appliedState struct {
	AppliedAt string `json:"appliedAt"`
	Base      string `json:"base"`
	Model     string `json:"model"`
	Backup    string `json:"backup"`
}

func dataDir() string {
	if v := os.Getenv("ASTER_ONBOARD_DATA"); v != "" {
		return v
	}
	return filepath.Join(appSupportDir(homeDir()), "aster-onboard")
}

func backupsRoot(client string) string {
	return filepath.Join(dataDir(), "backups", client)
}

func statePath() string { return filepath.Join(dataDir(), "state.json") }

func loadState() map[string]appliedState {
	raw, err := os.ReadFile(statePath())
	if err != nil {
		return map[string]appliedState{}
	}
	var v map[string]appliedState
	if json.Unmarshal(raw, &v) != nil {
		return map[string]appliedState{}
	}
	return v
}

func saveState(state map[string]appliedState) error {
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath(), raw, 0o644)
}

// createBackup 快照 client 的全部目标文件，返回版本 id（时间戳）。
func createBackup(client string, paths []string, base, model string) (string, error) {
	id := time.Now().Format("20060102-150405")
	dir := filepath.Join(backupsRoot(client), id)
	meta := backupMeta{ID: id, Client: client, CreatedAt: time.Now().Format(time.RFC3339), Base: base, Model: model}
	for i, path := range paths {
		name := fmt.Sprintf("file%d%s", i, filepath.Ext(path))
		raw, err := os.ReadFile(path)
		if err != nil {
			meta.Files = append(meta.Files, backupFile{Path: path, Existed: false, Name: name})
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
			return "", err
		}
		meta.Files = append(meta.Files, backupFile{Path: path, Existed: true, Name: name})
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), raw, 0o644); err != nil {
		return "", err
	}
	return id, nil
}

func listBackups(client string) []backupMeta {
	entries, err := os.ReadDir(backupsRoot(client))
	if err != nil {
		return nil
	}
	var out []backupMeta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(backupsRoot(client), e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		var meta backupMeta
		if json.Unmarshal(raw, &meta) != nil {
			continue
		}
		out = append(out, meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// latestBackup 最近一次备份（写入前的快照）。
func latestBackup(client string) *backupMeta {
	all := listBackups(client)
	if len(all) == 0 {
		return nil
	}
	return &all[0]
}

// HasAfter 这份备份里有没有"写入后"快照。
func (m backupMeta) HasAfter() bool {
	for _, f := range m.Files {
		if f.After != "" {
			return true
		}
	}
	return false
}

// latestWrittenBackup 最近一次"成功写入"留下的备份（含写入后快照）。
func latestWrittenBackup(client string) *backupMeta {
	for _, b := range listBackups(client) { // 已按时间倒序
		if b.HasAfter() {
			return &b
		}
	}
	return nil
}

// saveAfterSnapshot 写入成功后把"写出去的内容"另存一份进本备份目录，并回填 meta。
// 「恢复」按钮靠它把配置还原成工具写好的样子（不依赖密钥还留着）。
func saveAfterSnapshot(client, id string, plan map[string]string) error {
	dir := filepath.Join(backupsRoot(client), id)
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return err
	}
	var meta backupMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return err
	}
	for i := range meta.Files {
		content, ok := plan[meta.Files[i].Path]
		if !ok {
			continue
		}
		name := fmt.Sprintf("file%d.after%s", i, filepath.Ext(meta.Files[i].Path))
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return err
		}
		meta.Files[i].After = name
	}
	out, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), out, 0o644)
}

// restoreBackup 按快照还原：after=true 用"写入后"快照（恢复成工具写好的配置），
// after=false 用"写入前"快照（撤销写入；原本不存在的文件会被删掉）。
func restoreBackup(client, id string, after bool) error {
	dir := filepath.Join(backupsRoot(client), id)
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return fmt.Errorf("找不到备份 %s：%w", id, err)
	}
	var meta backupMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return err
	}
	if after && !meta.HasAfter() {
		return fmt.Errorf("这份备份里没有写入后的快照（工具没写过这个客户端）")
	}
	restored := 0
	for _, f := range meta.Files {
		name := f.Name
		if after {
			name = f.After
			if name == "" {
				continue
			}
		} else if !f.Existed {
			os.Remove(f.Path)
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("备份文件缺失 %s：%w", name, err)
		}
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(f.Path, content, 0o644); err != nil {
			return err
		}
		restored++
	}
	if after && restored == 0 {
		return fmt.Errorf("没有可还原的文件")
	}
	state := loadState()
	if after {
		// 恢复成工具写入的样子：客户端仍是托管的，状态保留。
	} else {
		delete(state, client) // 撤销写入：回到"未托管"
	}
	return saveState(state)
}

// applyClient 备份 → 写配置 → 记状态，任一步失败不留半截状态。
func applyClient(c Client, p Params) (string, error) {
	plan, err := c.Build(homeDir(), p)
	if err != nil {
		return "", err
	}
	backupID, err := createBackup(c.ID, c.Paths(homeDir()), p.Base, p.Model)
	if err != nil {
		return "", err
	}
	for path, content := range plan {
		if err := writeDirFor(path); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return "", fmt.Errorf("写入 %s 失败：%w", path, err)
		}
	}
	// 另存一份"写入结果"，「恢复」按钮用它把配置还原成工具写好的样子；
	// 快照失败不影响这次写入，只是少一份恢复依据。
	_ = saveAfterSnapshot(c.ID, backupID, plan)
	state := loadState()
	state[c.ID] = appliedState{
		AppliedAt: time.Now().Format(time.RFC3339),
		Base:      p.Base,
		Model:     p.Model,
		Backup:    backupID,
	}
	if err := saveState(state); err != nil {
		return "", err
	}
	return backupID, nil
}
