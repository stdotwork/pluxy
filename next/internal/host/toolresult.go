package host

import (
	"fmt"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// toolResultPartsMinor is the protocol minor version that adds a tool result's parts
// (llm.Item.Content on a tool_result) and its error flag (llm.Item.IsError).
const toolResultPartsMinor = 25

// downgradeToolResults returns req as an extension that implements protocol minor version minor
// can take it. Before toolResultPartsMinor each tool result keeps its text, in Output, and loses
// its parts and error flag, returned as dropped losses: an extension that cannot read them would
// leave them out without saying so. req's own items are left as they are; a changed request gets
// its own copy of them.
func downgradeToolResults(minor int, id string, req llm.Request) (llm.Request, []llm.Loss) {
	if minor >= toolResultPartsMinor {
		return req, nil
	}
	detail := fmt.Sprintf("extension %s implements protocol 0.%d, which carries only a tool result's text (0.%d adds its parts and error flag)", id, minor, toolResultPartsMinor)
	var losses []llm.Loss
	var items []llm.Item
	for i, it := range req.Items {
		if it.Type != llm.ItemTypeToolResult || (len(it.Content) == 0 && !it.IsError) {
			continue
		}
		if items == nil {
			items = slices.Clone(req.Items)
		}
		path := fmt.Sprintf("items[%d]", i)
		if it.Origin != nil && it.Origin.Path != "" {
			path = it.Origin.Path
		}
		for k, p := range it.Content {
			// A text part's text is in Output; only fields of its own are lost with it.
			if p.Type == llm.PartText && (p.Origin == nil || len(p.Origin.Raw) == 0) {
				continue
			}
			pp := fmt.Sprintf("%s.content[%d]", path, k)
			if p.Origin != nil && p.Origin.Path != "" {
				pp = p.Origin.Path
			}
			losses = llm.AddLoss(losses, llm.Loss{Kind: llm.LossDropped, Path: pp, Detail: detail})
		}
		if it.IsError {
			losses = llm.AddLoss(losses, llm.Loss{Kind: llm.LossDropped, Path: path + ".is_error", Detail: detail})
		}
		items[i].Content, items[i].IsError = nil, false
	}
	if items != nil {
		req.Items = items
	}
	return req, losses
}

// refuseDowngrade returns the error for a request whose tool results lost parts on the way to an
// extension before toolResultPartsMinor, when its drop policy is reject; otherwise nil.
func refuseDowngrade(id string, minor int, req llm.Request, lost []llm.Loss) *llm.Error {
	if req.DropPolicy != llm.DropReject || len(lost) == 0 {
		return nil
	}
	paths := make([]string, 0, len(lost))
	for _, l := range lost {
		paths = append(paths, l.Path)
	}
	return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Dropped: paths,
		Message: fmt.Sprintf("extension %s implements protocol 0.%d, which cannot carry a tool result's images, files or error flag (0.%d adds them): %s",
			id, minor, toolResultPartsMinor, strings.Join(paths, ", "))}
}

// withLosses adds lost to the translation report of a response.start event, as losses of the
// extension's own translation. A reply sent as the client's body unchanged lost nothing.
func withLosses(ev llm.Event, lost []llm.Loss) llm.Event {
	if len(lost) == 0 || ev.Type != llm.EventResponseStart {
		return ev
	}
	var r llm.ResponseInfo
	if ev.Response != nil {
		r = *ev.Response
	}
	var t llm.Translation
	if r.Translation != nil {
		if r.Translation.Passthrough {
			return ev
		}
		t = *r.Translation
		t.Losses = slices.Clone(t.Losses)
	}
	for _, l := range lost {
		t.Losses = llm.AddLoss(t.Losses, l)
	}
	r.Translation = &t
	ev.Response = &r
	return ev
}
