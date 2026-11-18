package main

import (
	"errors"
	"strings"
	"testing"
)

// fixedPane is an orphanPane whose capture is pinned, to drive the composer
// guard into a specific refusal.
type fixedPane struct {
	*orphanPane
	raw string
	err error
}

func (p *fixedPane) CapturePaneFresh() (string, error) { return p.raw, p.err }

func TestSendBlocked_MidTurnAdvisesDeferIfBusy(t *testing.T) {
	pane := &fixedPane{orphanPane: &orphanPane{}, raw: "· Thinking… (esc to interrupt)\n[Pasted text #1 +42 lines]\n"}
	res, err := executeSend(pane, "claude", "[inbox] next", false, residueTuning(false, newMemResidueStore()))
	if err == nil || res.delivery != "composer_blocked" {
		t.Fatalf("delivery=%q err=%v, want composer_blocked", res.delivery, err)
	}
	if !strings.Contains(err.Error(), "--defer-if-busy") {
		t.Fatalf("err %q does not point at --defer-if-busy", err)
	}
	if got := res.jsonFields()["composer_block_reason"]; got != "target_mid_turn" {
		t.Fatalf("composer_block_reason = %v, want target_mid_turn", got)
	}
}

func TestSendBlocked_UnreadablePaneSaysCheckTheSession(t *testing.T) {
	pane := &fixedPane{orphanPane: &orphanPane{}, err: errors.New("pane gone")}
	res, err := executeSend(pane, "claude", "[inbox] next", false, residueTuning(false, newMemResidueStore()))
	if err == nil || res.delivery != "composer_blocked" {
		t.Fatalf("delivery=%q err=%v, want composer_blocked", res.delivery, err)
	}
	if !strings.Contains(err.Error(), "could not be captured") {
		t.Fatalf("err %q does not say the pane could not be captured", err)
	}
	if got := res.jsonFields()["composer_block_reason"]; got != "pane_unreadable" {
		t.Fatalf("composer_block_reason = %v, want pane_unreadable", got)
	}
}

// When the box's contents can be named, that description stays: the cause
// only adds the machine-readable field.
func TestSendBlocked_OperatorDraftKeepsTheContentDescription(t *testing.T) {
	const human = "run `go build` then report"
	inner := &orphanPane{segs: []orphanSeg{{lit: human}}}
	res, err := executeSend(&orphanPaneBS{orphanPane: inner}, "claude", "[inbox] next", false, residueTuning(false, newMemResidueStore()))
	if err == nil || !strings.Contains(err.Error(), "other text (a person's draft?)") {
		t.Fatalf("err=%v, want the other-text description", err)
	}
	if got := res.jsonFields()["composer_block_reason"]; got != "operator_draft" {
		t.Fatalf("composer_block_reason = %v, want operator_draft", got)
	}
}
