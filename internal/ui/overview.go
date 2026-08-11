package ui

import (
	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/workstream"
)

// overviewModel is the UI's small indexed read model. Both primary surfaces
// consume the same relationship projection and differ only in local collapse
// state; neither reconstructs workstream membership independently.
type overviewModel struct {
	workstreams          []workstream.Workstream
	sessions             []control.Session
	orphans              []control.Session
	workstreamByID       map[string]workstream.Workstream
	sessionByID          map[string]control.Session
	sessionsByWorkstream map[string][]control.Session
}

func newOverviewModel(snapshot control.Snapshot) overviewModel {
	view := overviewModel{
		workstreams:          append([]workstream.Workstream(nil), snapshot.Workstreams...),
		sessions:             append([]control.Session(nil), snapshot.Sessions...),
		orphans:              append([]control.Session(nil), snapshot.Orphans...),
		workstreamByID:       make(map[string]workstream.Workstream, len(snapshot.Workstreams)),
		sessionByID:          make(map[string]control.Session, len(snapshot.Sessions)+len(snapshot.Orphans)),
		sessionsByWorkstream: make(map[string][]control.Session, len(snapshot.Workstreams)+1),
	}
	for _, item := range view.workstreams {
		view.workstreamByID[item.ID] = item
	}
	for _, session := range view.sessions {
		view.sessionByID[session.ID] = session
		view.sessionsByWorkstream[session.WorkstreamID] = append(view.sessionsByWorkstream[session.WorkstreamID], session)
	}
	for _, session := range view.orphans {
		view.sessionByID[session.ID] = session
	}
	return view
}

func (m *Model) setSnapshot(snapshot control.Snapshot) {
	m.snapshot = snapshot
	m.overview = newOverviewModel(snapshot)
}

func (o overviewModel) rows(collapsed map[string]bool) []listRow {
	rows := make([]listRow, 0, len(o.workstreams)+len(o.sessions)+len(o.orphans)+2)
	for _, item := range o.workstreams {
		key := workstreamRowKey(item.ID)
		rows = append(rows, listRow{key: key, kind: rowWorkstream, workstreamID: item.ID})
		if collapsed[key] {
			continue
		}
		for _, session := range o.sessionsByWorkstream[item.ID] {
			rows = append(rows, listRow{key: sessionRowKey(session), kind: rowSession, workstreamID: item.ID, sessionID: session.ID})
		}
	}
	rows = append(rows, listRow{key: ungroupedKey, kind: rowWorkstream})
	if !collapsed[ungroupedKey] {
		for _, session := range o.sessionsByWorkstream[""] {
			rows = append(rows, listRow{key: sessionRowKey(session), kind: rowSession, sessionID: session.ID})
		}
	}
	if len(o.orphans) > 0 {
		rows = append(rows, listRow{key: orphanedKey, kind: rowOrphanHeader})
		if !collapsed[orphanedKey] {
			for _, session := range o.orphans {
				rows = append(rows, listRow{key: sessionRowKey(session), kind: rowOrphan, sessionID: session.ID})
			}
		}
	}
	return rows
}

func (o overviewModel) session(id string) (control.Session, bool) {
	session, ok := o.sessionByID[id]
	return session, ok
}

func (o overviewModel) workstream(id string) (workstream.Workstream, bool) {
	item, ok := o.workstreamByID[id]
	return item, ok
}

func (o overviewModel) sessionsFor(row listRow) []control.Session {
	if row.kind == rowOrphanHeader {
		return o.orphans
	}
	return o.sessionsByWorkstream[row.workstreamID]
}

func (o overviewModel) allSessions() []control.Session {
	all := make([]control.Session, 0, len(o.sessions)+len(o.orphans))
	all = append(all, o.sessions...)
	return append(all, o.orphans...)
}
