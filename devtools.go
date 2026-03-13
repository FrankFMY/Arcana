package arcana

import (
	"encoding/json"
	"net/http"
	"sort"
)

// DevToolsHandler returns an http.Handler that serves a DevTools dashboard
// for inspecting engine state. Mount with StripPrefix at your chosen path:
//
//	mux.Handle("/devtools/", http.StripPrefix("/devtools", engine.DevToolsHandler()))
func (e *Engine) DevToolsHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(e.devtoolsState())
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(devtoolsPage))
	})

	return mux
}

type devtoolsStateResponse struct {
	Running          bool                `json:"running"`
	Graphs           []string            `json:"graphs"`
	Mutations        []string            `json:"mutations"`
	Schema           map[string][]string `json:"schema"`
	Subscriptions    int                 `json:"subscriptions"`
	Seances          int                 `json:"seances"`
	DataStoreRows    int                 `json:"data_store_rows"`
	WSConnections    int                 `json:"ws_connections"`
	WSWorkspaces     int                 `json:"ws_workspaces"`
	ActiveSubs       []devtoolsSubInfo   `json:"active_subs"`
}

type devtoolsSubInfo struct {
	GraphKey   string `json:"graph_key"`
	ParamsHash string `json:"params_hash"`
	SeanceID   string `json:"seance_id"`
	Version    int64  `json:"version"`
}

func (e *Engine) devtoolsState() devtoolsStateResponse {
	stats := e.Stats()

	resp := devtoolsStateResponse{
		Running:       stats.Running,
		Graphs:        e.registry.Keys(),
		Mutations:     e.registry.MutationKeys(),
		Schema:        e.registry.RepTable(),
		Subscriptions: stats.ActiveSubscriptions,
		Seances:       stats.SeancesWithSubs,
		DataStoreRows: stats.DataStoreRows,
	}

	if e.wsTransport != nil {
		conns, workspaces := e.wsTransport.ConnStats()
		resp.WSConnections = conns
		resp.WSWorkspaces = workspaces
	}

	if e.manager != nil {
		resp.ActiveSubs = e.collectActiveSubs()
	}

	return resp
}

func (e *Engine) collectActiveSubs() []devtoolsSubInfo {
	e.manager.mu.RLock()
	defer e.manager.mu.RUnlock()

	subs := make([]devtoolsSubInfo, 0, len(e.manager.subs))
	for _, sub := range e.manager.subs {
		subs = append(subs, devtoolsSubInfo{
			GraphKey:   sub.GraphKey,
			ParamsHash: sub.ParamsHash,
			SeanceID:   sub.SeanceID,
			Version:    sub.Version,
		})
	}

	sort.Slice(subs, func(i, j int) bool {
		if subs[i].GraphKey != subs[j].GraphKey {
			return subs[i].GraphKey < subs[j].GraphKey
		}
		return subs[i].SeanceID < subs[j].SeanceID
	})

	return subs
}

// ConnStats returns connection count and workspace count for WSTransport.
func (t *WSTransport) ConnStats() (conns int, workspaces int) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.bySeance), len(t.byWorkspace)
}

const devtoolsPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Arcana DevTools</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{background:#0d1117;color:#c9d1d9;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;font-size:14px;padding:20px}
h1{color:#58a6ff;font-size:20px;margin-bottom:16px;display:flex;align-items:center;gap:8px}
.status{display:inline-block;width:10px;height:10px;border-radius:50%}
.status.on{background:#3fb950}.status.off{background:#f85149}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));gap:12px;margin-bottom:20px}
.card{background:#161b22;border:1px solid #30363d;border-radius:8px;padding:16px}
.card .label{color:#8b949e;font-size:12px;text-transform:uppercase;letter-spacing:.5px}
.card .value{color:#f0f6fc;font-size:28px;font-weight:600;margin-top:4px}
.section{margin-bottom:20px}
.section h2{color:#8b949e;font-size:13px;text-transform:uppercase;letter-spacing:.5px;margin-bottom:8px;border-bottom:1px solid #21262d;padding-bottom:4px}
.tag{display:inline-block;background:#1f6feb33;color:#58a6ff;border-radius:4px;padding:2px 8px;margin:2px;font-size:12px}
table{width:100%;border-collapse:collapse;background:#161b22;border:1px solid #30363d;border-radius:8px;overflow:hidden}
th{background:#0d1117;color:#8b949e;font-size:12px;text-transform:uppercase;letter-spacing:.5px;text-align:left;padding:8px 12px}
td{padding:8px 12px;border-top:1px solid #21262d;font-family:"SFMono-Regular",Consolas,"Liberation Mono",Menlo,monospace;font-size:13px}
tr:hover td{background:#1c2128}
.footer{color:#484f58;font-size:12px;margin-top:16px;text-align:center}
.schema-table{margin-bottom:8px}
.schema-name{color:#d2a8ff;font-weight:600;font-size:13px}
.schema-cols{color:#8b949e;font-size:12px;margin-left:8px}
</style>
</head>
<body>
<h1><span id="statusDot" class="status off"></span> Arcana DevTools</h1>
<div id="cards" class="grid"></div>
<div id="graphs" class="section"><h2>Graphs</h2><div id="graphTags"></div></div>
<div id="mutations" class="section"><h2>Mutations</h2><div id="mutationTags"></div></div>
<div id="schemaSection" class="section"><h2>Schema</h2><div id="schemaBody"></div></div>
<div id="subsSection" class="section"><h2>Active Subscriptions</h2><div id="subsBody"></div></div>
<div id="footer" class="footer"></div>
<script>
(function(){
  var stateUrl = window.location.pathname.replace(/\/$/, '') + '/state';

  function el(tag, props, children) {
    var e = document.createElement(tag);
    if (props) {
      for (var k in props) {
        if (k === 'textContent') e.textContent = props[k];
        else if (k === 'className') e.className = props[k];
        else e.setAttribute(k, props[k]);
      }
    }
    if (children) {
      for (var i = 0; i < children.length; i++) {
        if (typeof children[i] === 'string') e.appendChild(document.createTextNode(children[i]));
        else e.appendChild(children[i]);
      }
    }
    return e;
  }

  function clear(node) { while (node.firstChild) node.removeChild(node.firstChild); }

  function makeCard(label, value) {
    return el('div', {className:'card'}, [
      el('div', {className:'label', textContent: label}),
      el('div', {className:'value', textContent: String(value)})
    ]);
  }

  function renderTags(container, items) {
    clear(container);
    if (!items || items.length === 0) {
      container.appendChild(el('span', {textContent: 'None', className:'tag'}));
      return;
    }
    for (var i = 0; i < items.length; i++) {
      container.appendChild(el('span', {textContent: items[i], className:'tag'}));
    }
  }

  function renderSchema(container, schema) {
    clear(container);
    var tables = Object.keys(schema || {}).sort();
    if (tables.length === 0) {
      container.appendChild(el('span', {textContent: 'No tables', className:'tag'}));
      return;
    }
    for (var i = 0; i < tables.length; i++) {
      var row = el('div', {className:'schema-table'}, [
        el('span', {textContent: tables[i], className:'schema-name'}),
        el('span', {textContent: ' \u2014 ' + (schema[tables[i]] || []).join(', '), className:'schema-cols'})
      ]);
      container.appendChild(row);
    }
  }

  function renderSubs(container, subs) {
    clear(container);
    if (!subs || subs.length === 0) {
      container.appendChild(el('span', {textContent: 'No active subscriptions', className:'tag'}));
      return;
    }
    var tbl = el('table');
    var thead = el('thead');
    var headerRow = el('tr', null, [
      el('th', {textContent:'Graph'}),
      el('th', {textContent:'Seance'}),
      el('th', {textContent:'Params Hash'}),
      el('th', {textContent:'Version'})
    ]);
    thead.appendChild(headerRow);
    tbl.appendChild(thead);
    var tbody = el('tbody');
    for (var i = 0; i < subs.length; i++) {
      var s = subs[i];
      var tr = el('tr', null, [
        el('td', {textContent: s.graph_key}),
        el('td', {textContent: (s.seance_id || '').substring(0, 12) + '...'}),
        el('td', {textContent: (s.params_hash || '').substring(0, 16) + '...'}),
        el('td', {textContent: String(s.version)})
      ]);
      tbody.appendChild(tr);
    }
    tbl.appendChild(tbody);
    container.appendChild(tbl);
  }

  function update() {
    var xhr = new XMLHttpRequest();
    xhr.open('GET', stateUrl);
    xhr.onload = function() {
      if (xhr.status !== 200) return;
      try { var d = JSON.parse(xhr.responseText); } catch(e) { return; }

      var dot = document.getElementById('statusDot');
      dot.className = 'status ' + (d.running ? 'on' : 'off');

      var cards = document.getElementById('cards');
      clear(cards);
      cards.appendChild(makeCard('Subscriptions', d.subscriptions || 0));
      cards.appendChild(makeCard('Seances', d.seances || 0));
      cards.appendChild(makeCard('DataStore Rows', d.data_store_rows || 0));
      cards.appendChild(makeCard('WS Connections', d.ws_connections || 0));
      cards.appendChild(makeCard('WS Workspaces', d.ws_workspaces || 0));
      cards.appendChild(makeCard('Graphs', (d.graphs || []).length));
      cards.appendChild(makeCard('Mutations', (d.mutations || []).length));

      renderTags(document.getElementById('graphTags'), d.graphs);
      renderTags(document.getElementById('mutationTags'), d.mutations);
      renderSchema(document.getElementById('schemaBody'), d.schema);
      renderSubs(document.getElementById('subsBody'), d.active_subs);

      var footer = document.getElementById('footer');
      footer.textContent = 'Last updated: ' + new Date().toLocaleTimeString();
    };
    xhr.send();
  }

  update();
  setInterval(update, 2000);
})();
</script>
</body>
</html>`
