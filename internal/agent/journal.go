package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"verdict/internal/protocol"
)

// Journal 把每个测试的事件按顺序追加到 journal/<run_id>.jsonl。
// 先落盘再发送，断线重连后按后端回报的 last_seq 补发。
type Journal struct {
	dir string
	mu  sync.Mutex
	seq map[string]int64 // 已写入的最大 seq
}

func OpenJournal(dir string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Journal{dir: dir, seq: map[string]int64{}}, nil
}

func (j *Journal) path(runID string) string { return filepath.Join(j.dir, runID+".jsonl") }

// Append 分配 seq 并落盘，返回带 seq 的事件。
func (j *Journal) Append(ev protocol.Event) (protocol.Event, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.seq[ev.RunID]; !ok {
		evs, err := j.readLocked(ev.RunID)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return ev, err
		}
		if len(evs) > 0 {
			j.seq[ev.RunID] = evs[len(evs)-1].Seq
		}
	}
	j.seq[ev.RunID]++
	ev.Seq = j.seq[ev.RunID]
	ev.Type = protocol.TypeEvent
	if ev.TS == 0 {
		ev.TS = time.Now().UnixMilli()
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return ev, err
	}
	f, err := os.OpenFile(j.path(ev.RunID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return ev, err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return ev, err
	}
	return ev, nil
}

func (j *Journal) readLocked(runID string) ([]protocol.Event, error) {
	f, err := os.Open(j.path(runID))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []protocol.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var ev protocol.Event
		if json.Unmarshal(sc.Bytes(), &ev) == nil {
			out = append(out, ev)
		}
		// 写到一半被中断的末行直接丢弃。
	}
	return out, sc.Err()
}

// Since 返回 seq 大于 after 的事件。
func (j *Journal) Since(runID string, after int64) ([]protocol.Event, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	evs, err := j.readLocked(runID)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	i := 0
	for i < len(evs) && evs[i].Seq <= after {
		i++
	}
	return evs[i:], nil
}

// Runs 列出本地有记录的全部测试。
func (j *Journal) Runs() []string {
	ents, _ := os.ReadDir(j.dir)
	var out []string
	for _, e := range ents {
		if id, ok := strings.CutSuffix(e.Name(), ".jsonl"); ok {
			out = append(out, id)
		}
	}
	return out
}

// has 判断某测试的事件日志里是否有满足条件的事件。
func (j *Journal) has(runID string, match func(protocol.Event) bool) bool {
	evs, _ := j.Since(runID, 0)
	for _, ev := range evs {
		if match(ev) {
			return true
		}
	}
	return false
}

// Closed 判断某测试是否已收尾（有 closed 事件）。
func (j *Journal) Closed(runID string) bool {
	return j.has(runID, func(ev protocol.Event) bool { return ev.Kind == protocol.KindClosed })
}

// StageDone 判断某阶段是否已有结果。
func (j *Journal) StageDone(runID, stageID string) bool {
	return j.has(runID, func(ev protocol.Event) bool { return ev.Kind == protocol.KindStageResult && ev.StageID == stageID })
}

// Prune 删除早于 maxAge 且已结束的日志。
func (j *Journal) Prune(maxAge time.Duration) {
	for _, id := range j.Runs() {
		st, err := os.Stat(j.path(id))
		if err != nil || time.Since(st.ModTime()) < maxAge || !j.Closed(id) {
			continue
		}
		j.mu.Lock()
		_ = os.Remove(j.path(id))
		delete(j.seq, id)
		j.mu.Unlock()
	}
}
