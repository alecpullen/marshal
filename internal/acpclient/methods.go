package acpclient

// Method is one row of the seam's ACP mapping (C2, T4).
type Method struct {
	Iface  string // "Host" or "Session"
	Name   string // the interface method's name
	ACP    string // the ACP method that carries it; "" when it has none
	Exists bool   // acp.Server registers ACP today; false means new in P3
}

// Methods maps every method of Host and Session to the ACP method that carries
// it (C2, T4).
//
// Two tests keep it in step with the interfaces:
// TestMethodTableCoversTheInterfaces here, and
// TestACPClientMethodTableMatchesTheServer in internal/acp. A method added to
// an interface needs a row here, an "ACP:" line in its doc, and the same row on
// the server side.
var Methods = []Method{
	{Iface: "Host", Name: "SharedFilesystem"},
	{Iface: "Session", Name: "ID", ACP: "session/new", Exists: true},
	{Iface: "Session", Name: "Status", ACP: "session/status"},
	{Iface: "Session", Name: "Changed"},
	{Iface: "Session", Name: "SendNow", ACP: "session/steer", Exists: true},
	{Iface: "Session", Name: "Sheet", ACP: "session/sheet"},
	{Iface: "Session", Name: "RefreshSheet", ACP: "session/sheet"},
	{Iface: "Session", Name: "Stack", ACP: "session/stack", Exists: true},
	{Iface: "Session", Name: "StackNode", ACP: "session/stack_node", Exists: true},
	{Iface: "Session", Name: "Callers", ACP: "session/callers"},
	{Iface: "Session", Name: "Pending", ACP: "session/request_permission"},
	{Iface: "Session", Name: "AnswerApproval", ACP: "session/request_permission"},
	{Iface: "Session", Name: "AnswerQuestion", ACP: "session/request_question"},
	{Iface: "Session", Name: "AnswerSkillGate", ACP: "session/request_permission"},
	{Iface: "Session", Name: "AnswerSudo", ACP: "session/request_secret"},
	{Iface: "Session", Name: "ReconcileAccess"},
	{Iface: "Session", Name: "AnswerAccess", ACP: "session/request_permission"},
	{Iface: "Session", Name: "Rollback", ACP: "session/rollback"},
}
