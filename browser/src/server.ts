// Thin Playwright bridge — exposes browser actions over local RPC.
// The Go core sends commands (navigate, click, fill, screenshot, readPage)
// and this process executes them. Zero decision-making logic lives here.