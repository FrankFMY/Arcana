// Example: real-time dashboard with WebSocket transport, mutations, and DevTools.
//
// Demonstrates the v0.2.0 zero-dependency setup:
//   - Built-in WSTransport (no Centrifugo needed)
//   - Mutations for server-side writes via WS or HTTP
//   - DevTools panel for runtime inspection
//
// Run:
//
//	go run ./examples/dashboard
//
// Then open:
//   - http://localhost:8080/             — dashboard UI
//   - http://localhost:8080/devtools/    — DevTools panel
//   - ws://localhost:8080/ws             — WebSocket endpoint
package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/FrankFMY/arcana"
)

// In-memory store simulating a database.
var (
	mu    sync.RWMutex
	tasks = map[string]map[string]any{
		"t1": {"id": "t1", "title": "Set up CI pipeline", "status": "done", "assignee": "Alice"},
		"t2": {"id": "t2", "title": "Write API docs", "status": "in_progress", "assignee": "Bob"},
		"t3": {"id": "t3", "title": "Fix login bug", "status": "todo", "assignee": "Alice"},
		"t4": {"id": "t4", "title": "Deploy to staging", "status": "todo", "assignee": "Charlie"},
	}
	metrics = map[string]map[string]any{
		"m1": {"id": "m1", "name": "CPU Usage", "value": "42%", "trend": "up"},
		"m2": {"id": "m2", "name": "Memory", "value": "67%", "trend": "stable"},
		"m3": {"id": "m3", "name": "Active Users", "value": "128", "trend": "up"},
		"m4": {"id": "m4", "name": "Error Rate", "value": "0.3%", "trend": "down"},
	}
)

// mockQuerier satisfies the Querier interface without a real database.
type mockQuerier struct{}

func (m *mockQuerier) Query(_ context.Context, _ string, _ ...any) (arcana.Rows, error) {
	return nil, nil
}
func (m *mockQuerier) QueryRow(_ context.Context, _ string, _ ...any) arcana.Row { return nil }

// Graph: task list
var TaskList = arcana.GraphDef{
	Key: "task_list",
	Params: arcana.ParamSchema{
		"workspace_id": arcana.ParamUUID().Required(),
	},
	Deps: []arcana.TableDep{
		{Table: "tasks", Columns: []string{"id", "title", "status", "assignee"}},
	},
	Factory: func(_ context.Context, _ arcana.Querier, _ arcana.Params) (*arcana.Result, error) {
		mu.RLock()
		defer mu.RUnlock()

		r := arcana.NewResult()
		for id, task := range tasks {
			r.AddRow("tasks", id, task)
			r.AddRef(arcana.Ref{
				Table:  "tasks",
				ID:     id,
				Fields: []string{"id", "title", "status", "assignee"},
			})
		}
		return r, nil
	},
}

// Graph: live metrics
var MetricsDashboard = arcana.GraphDef{
	Key: "metrics",
	Params: arcana.ParamSchema{
		"workspace_id": arcana.ParamUUID().Required(),
	},
	Deps: []arcana.TableDep{
		{Table: "metrics", Columns: []string{"id", "name", "value", "trend"}},
	},
	Factory: func(_ context.Context, _ arcana.Querier, _ arcana.Params) (*arcana.Result, error) {
		mu.RLock()
		defer mu.RUnlock()

		r := arcana.NewResult()
		for id, metric := range metrics {
			r.AddRow("metrics", id, metric)
			r.AddRef(arcana.Ref{
				Table:  "metrics",
				ID:     id,
				Fields: []string{"id", "name", "value", "trend"},
			})
		}
		return r, nil
	},
}

// Mutation: update task status
var UpdateTaskStatus = arcana.MutationDef{
	Key: "update_task_status",
	Params: arcana.ParamSchema{
		"task_id": arcana.ParamString().Required(),
		"status":  arcana.ParamString().Required(),
	},
	Handler: func(_ context.Context, _ arcana.Querier, p arcana.Params) (*arcana.MutationResult, error) {
		taskID := p.String("task_id")
		status := p.String("status")

		mu.Lock()
		task, ok := tasks[taskID]
		if !ok {
			mu.Unlock()
			return nil, fmt.Errorf("task %q not found", taskID)
		}
		task["status"] = status
		mu.Unlock()

		return &arcana.MutationResult{
			Data: map[string]any{"task_id": taskID, "status": status},
			Changes: []arcana.Change{
				{Table: "tasks", RowID: taskID, Columns: []string{"status"}},
			},
		}, nil
	},
}

func main() {
	engine := arcana.New(arcana.Config{
		Pool: &mockQuerier{},
		AuthFunc: func(r *http.Request) (*arcana.Identity, error) {
			return &arcana.Identity{
				SeanceID:    r.Header.Get("X-Seance-ID"),
				UserID:      "demo-user",
				WorkspaceID: "demo-workspace",
			}, nil
		},
		WSConfig: &arcana.WSTransportConfig{
			TokenAuthFunc: func(token string) (*arcana.Identity, error) {
				return &arcana.Identity{
					SeanceID:    fmt.Sprintf("ws-%d", time.Now().UnixNano()),
					UserID:      "demo-user",
					WorkspaceID: "demo-workspace",
				}, nil
			},
			AcceptOptions: nil,
		},
		GCInterval: 30 * time.Second,
	})

	if err := engine.Register(TaskList, MetricsDashboard); err != nil {
		log.Fatal(err)
	}
	if err := engine.RegisterMutation(UpdateTaskStatus); err != nil {
		log.Fatal(err)
	}
	if err := engine.Start(context.Background()); err != nil {
		log.Fatal(err)
	}
	defer engine.Stop()

	// Simulate live metric updates every 3 seconds
	go simulateMetrics(engine)

	mux := http.NewServeMux()

	// Arcana HTTP API
	mux.Handle("/arcana/", http.StripPrefix("/arcana", engine.Handler()))

	// WebSocket endpoint
	mux.Handle("/ws", engine.WSHandler())

	// DevTools panel
	mux.Handle("/devtools/", http.StripPrefix("/devtools", engine.DevToolsHandler()))

	// Dashboard UI
	mux.HandleFunc("/", serveDashboard)

	fmt.Println("Dashboard running on http://localhost:8080")
	fmt.Println("DevTools at http://localhost:8080/devtools/")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

func simulateMetrics(engine *arcana.Engine) {
	trends := []string{"up", "down", "stable"}
	for {
		time.Sleep(3 * time.Second)

		mu.Lock()
		for id, m := range metrics {
			switch m["name"] {
			case "CPU Usage":
				m["value"] = fmt.Sprintf("%d%%", 30+rand.Intn(40))
			case "Memory":
				m["value"] = fmt.Sprintf("%d%%", 50+rand.Intn(30))
			case "Active Users":
				m["value"] = fmt.Sprintf("%d", 100+rand.Intn(100))
			case "Error Rate":
				m["value"] = fmt.Sprintf("%.1f%%", float64(rand.Intn(20))/10.0)
			}
			m["trend"] = trends[rand.Intn(len(trends))]

			engine.NotifyTable(context.Background(), "metrics", id, []string{"value", "trend"})
		}
		mu.Unlock()
	}
}

func serveDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(dashboardHTML))
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Arcana Dashboard Example</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{background:#0d1117;color:#c9d1d9;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;padding:24px}
h1{color:#58a6ff;font-size:22px;margin-bottom:4px}
.subtitle{color:#8b949e;font-size:13px;margin-bottom:20px}
.status-bar{display:flex;align-items:center;gap:8px;margin-bottom:20px;padding:8px 12px;background:#161b22;border:1px solid #30363d;border-radius:6px;font-size:13px}
.dot{width:8px;height:8px;border-radius:50%}
.dot.connected{background:#3fb950}.dot.connecting{background:#d29922}.dot.disconnected{background:#f85149}
.metrics{display:grid;grid-template-columns:repeat(4,1fr);gap:12px;margin-bottom:24px}
.metric{background:#161b22;border:1px solid #30363d;border-radius:8px;padding:16px;text-align:center}
.metric .name{color:#8b949e;font-size:12px;text-transform:uppercase}
.metric .val{font-size:32px;font-weight:700;color:#f0f6fc;margin:4px 0}
.metric .trend{font-size:12px}
.trend-up{color:#3fb950}.trend-down{color:#f85149}.trend-stable{color:#d29922}
h2{color:#c9d1d9;font-size:16px;margin-bottom:12px}
table{width:100%;border-collapse:collapse;background:#161b22;border:1px solid #30363d;border-radius:8px;overflow:hidden}
th{background:#0d1117;color:#8b949e;font-size:12px;text-transform:uppercase;text-align:left;padding:10px 14px}
td{padding:10px 14px;border-top:1px solid #21262d}
tr:hover td{background:#1c2128}
select{background:#21262d;color:#c9d1d9;border:1px solid #30363d;border-radius:4px;padding:4px 8px;font-size:13px;cursor:pointer}
select:focus{outline:none;border-color:#58a6ff}
.badge{display:inline-block;padding:2px 8px;border-radius:10px;font-size:12px;font-weight:500}
.badge-done{background:#23883833;color:#3fb950}
.badge-in_progress{background:#d2992233;color:#d29922}
.badge-todo{background:#8b949e33;color:#8b949e}
.footer{text-align:center;color:#484f58;font-size:12px;margin-top:24px}
.footer a{color:#58a6ff;text-decoration:none}
</style>
</head>
<body>
<h1>Arcana Dashboard</h1>
<p class="subtitle">Real-time sync with WebSocket transport, mutations, and DevTools</p>
<div class="status-bar"><span id="dot" class="dot disconnected"></span><span id="statusText">Connecting...</span></div>

<div id="metricsGrid" class="metrics"></div>

<h2>Tasks</h2>
<table>
<thead><tr><th>Title</th><th>Assignee</th><th>Status</th></tr></thead>
<tbody id="taskBody"></tbody>
</table>

<div class="footer">
<a href="/devtools/" target="_blank">Open DevTools</a>
</div>

<script>
(function(){
  var API = '/arcana';
  var WS_URL = 'ws://' + location.host + '/ws';
  var WORKSPACE = '550e8400-e29b-41d4-a716-446655440000';

  var ws = null;
  var seq = 0;
  var pending = {};
  var taskRefs = [];
  var metricRefs = [];
  var tables = {};

  function nextId() { return String(++seq); }

  function el(tag, props, children) {
    var e = document.createElement(tag);
    if (props) {
      for (var k in props) {
        if (k === 'textContent') e.textContent = props[k];
        else if (k === 'className') e.className = props[k];
        else if (k.indexOf('on') === 0) e.addEventListener(k.slice(2).toLowerCase(), props[k]);
        else e.setAttribute(k, props[k]);
      }
    }
    if (children) {
      for (var i = 0; i < children.length; i++) {
        if (typeof children[i] === 'string') e.appendChild(document.createTextNode(children[i]));
        else if (children[i]) e.appendChild(children[i]);
      }
    }
    return e;
  }

  function clear(node) { while (node.firstChild) node.removeChild(node.firstChild); }

  function setStatus(status) {
    var dot = document.getElementById('dot');
    var text = document.getElementById('statusText');
    dot.className = 'dot ' + status;
    text.textContent = status.charAt(0).toUpperCase() + status.slice(1);
  }

  function getRow(table, id) {
    var t = tables[table];
    return t ? t[id] : null;
  }

  function mergeTable(tableName, rows) {
    if (!tables[tableName]) tables[tableName] = {};
    for (var rowId in rows) {
      if (!tables[tableName][rowId]) tables[tableName][rowId] = {};
      var fields = rows[rowId];
      for (var f in fields) {
        tables[tableName][rowId][f] = fields[f];
      }
    }
  }

  function renderMetrics() {
    var grid = document.getElementById('metricsGrid');
    clear(grid);
    for (var i = 0; i < metricRefs.length; i++) {
      var ref = metricRefs[i];
      var row = getRow(ref.table, ref.id);
      if (!row) continue;

      var trendClass = 'trend-' + (row.trend || 'stable');
      var trendSymbol = row.trend === 'up' ? '\u2191' : row.trend === 'down' ? '\u2193' : '\u2192';

      var card = el('div', {className:'metric'}, [
        el('div', {className:'name', textContent: String(row.name || '')}),
        el('div', {className:'val', textContent: String(row.value || '')}),
        el('div', {className:'trend ' + trendClass, textContent: trendSymbol + ' ' + String(row.trend || '')})
      ]);
      grid.appendChild(card);
    }
  }

  function renderTasks() {
    var tbody = document.getElementById('taskBody');
    clear(tbody);
    for (var i = 0; i < taskRefs.length; i++) {
      var ref = taskRefs[i];
      var row = getRow(ref.table, ref.id);
      if (!row) continue;

      var statusClass = 'badge badge-' + String(row.status || 'todo');
      var statusText = String(row.status || 'todo').replace('_', ' ');
      var taskId = String(row.id || ref.id);

      var sel = el('select', {}, [
        el('option', {value:'todo', textContent:'todo'}),
        el('option', {value:'in_progress', textContent:'in progress'}),
        el('option', {value:'done', textContent:'done'})
      ]);
      sel.value = String(row.status || 'todo');
      (function(tid, select){
        select.addEventListener('change', function() {
          mutate('update_task_status', {task_id: tid, status: select.value});
        });
      })(taskId, sel);

      var tr = el('tr', null, [
        el('td', {textContent: String(row.title || '')}),
        el('td', {textContent: String(row.assignee || '')}),
        el('td', null, [sel])
      ]);
      tbody.appendChild(tr);
    }
  }

  function sendWS(msg) {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(msg));
    }
  }

  function request(msg) {
    return new Promise(function(resolve, reject) {
      var id = nextId();
      msg.id = id;
      pending[id] = {resolve: resolve, reject: reject};
      sendWS(msg);
      setTimeout(function() {
        if (pending[id]) {
          pending[id].reject(new Error('timeout'));
          delete pending[id];
        }
      }, 10000);
    });
  }

  function mutate(action, params) {
    request({type:'mutate', action:action, params:params}).catch(function(e){
      console.error('mutate error:', e);
    });
  }

  function subscribeView(view) {
    return request({type:'subscribe', view:view, params:{workspace_id: WORKSPACE}});
  }

  function handleMessage(raw) {
    var msg;
    try { msg = JSON.parse(raw); } catch(e) { return; }

    if (msg.reply_to && pending[msg.reply_to]) {
      var p = pending[msg.reply_to];
      delete pending[msg.reply_to];
      if (msg.type === 'error') p.reject(new Error(msg.message || 'error'));
      else p.resolve(msg);
      return;
    }

    if (msg.type === 'table_diff' && msg.data) {
      var diff = msg.data;
      for (var tableName in diff) {
        if (!tables[tableName]) tables[tableName] = {};
        var rows = diff[tableName];
        for (var rowId in rows) {
          var patches = rows[rowId];
          if (!tables[tableName][rowId]) tables[tableName][rowId] = {};
          for (var pi = 0; pi < patches.length; pi++) {
            var op = patches[pi];
            if (op.op === 'replace' || op.op === 'add') {
              var field = op.path.replace(/^\//, '');
              tables[tableName][rowId][field] = op.value;
            }
          }
        }
      }
      renderMetrics();
      renderTasks();
    }

    if (msg.type === 'view_diff') {
      renderMetrics();
      renderTasks();
    }
  }

  function connect() {
    setStatus('connecting');
    ws = new WebSocket(WS_URL);

    ws.onopen = function() {
      request({type:'auth', token:'demo-token'}).then(function(reply) {
        setStatus('connected');
        return subscribeView('task_list');
      }).then(function(reply) {
        if (reply.refs) taskRefs = reply.refs;
        if (reply.tables) {
          for (var t in reply.tables) mergeTable(t, reply.tables[t]);
        }
        renderTasks();
        return subscribeView('metrics');
      }).then(function(reply) {
        if (reply.refs) metricRefs = reply.refs;
        if (reply.tables) {
          for (var t in reply.tables) mergeTable(t, reply.tables[t]);
        }
        renderMetrics();
      }).catch(function(e) {
        console.error('setup error:', e);
      });
    };

    ws.onmessage = function(e) { handleMessage(e.data); };

    ws.onclose = function() {
      setStatus('disconnected');
      setTimeout(connect, 2000);
    };
  }

  connect();
})();
</script>
</body>
</html>`
