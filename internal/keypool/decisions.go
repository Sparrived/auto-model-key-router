package keypool

import "github.com/Sparrived/auto-model-key-router/internal/config"

// Decisions 是调用方私有的固定容量缓冲，只在真实判定点写入。不保存密钥、URL，也不在池锁内发通知。
type Decisions struct {
	Items   [64]Decision
	Count   int
	Omitted int
}

type Decision struct {
	Kind, Reason, Mode, Phase        string
	Provider, KeyName, UpstreamModel string
}

func (d *Decisions) candidate(key config.KeyConfig, reason, phase string) {
	d.add(key, "candidate", reason, "", phase)
}

func (d *Decisions) selection(key config.KeyConfig, mode, reason, phase string, eligible []config.KeyConfig) {
	if d == nil {
		return
	}
	d.add(key, "selection", reason, mode, phase)
	for _, other := range eligible {
		if other.Name != key.Name {
			d.candidate(other, "eligible_not_selected", phase)
		}
	}
}

func (d *Decisions) add(key config.KeyConfig, kind, reason, mode, phase string) {
	if d == nil {
		return
	}
	if d.Count == len(d.Items) {
		d.Omitted++
		return
	}
	name, provider, model := key.Name, key.Provider, key.UpstreamModel
	if name == key.APIKey {
		name = ""
	}
	if provider == key.APIKey {
		provider = ""
	}
	if model == key.APIKey {
		model = ""
	}
	d.Items[d.Count] = Decision{Kind: kind, Reason: reason, Mode: mode, Phase: phase, Provider: provider, KeyName: name, UpstreamModel: model}
	d.Count++
}
