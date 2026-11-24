package geminigenerate

// pendingCodes holds the executableCode parts still waiting for a result, oldest first. Taking one
// costs the same however many wait, so a reply of many runs decodes in linear time.
type pendingCodes struct {
	items []pendingExecCode // in arrival order; a taken one is cleared and marked gone
	head  int               // every item before head is gone
	live  int               // items not gone
	byID  map[string][]int  // positions of the live items with each non-empty ID, oldest first
	noID  int               // 1 + position of the live item with no ID, 0 if none
}

func (p *pendingCodes) len() int { return p.live }

// add appends pc as the newest.
func (p *pendingCodes) add(pc pendingExecCode) {
	pos := len(p.items)
	p.items = append(p.items, pc)
	p.live++
	if pc.id == "" {
		p.noID = pos + 1
		return
	}
	if p.byID == nil {
		p.byID = map[string][]int{}
	}
	p.byID[pc.id] = append(p.byID[pc.id], pos)
}

// dropNoID removes the code with no ID, reporting whether there was one.
func (p *pendingCodes) dropNoID() bool {
	if p.noID == 0 {
		return false
	}
	p.remove(p.noID - 1)
	p.noID = 0
	return true
}

// takeByID removes and returns the oldest code with this non-empty ID.
func (p *pendingCodes) takeByID(id string) (pendingExecCode, bool) {
	q := p.byID[id]
	if id == "" || len(q) == 0 {
		return pendingExecCode{}, false
	}
	if len(q) == 1 {
		delete(p.byID, id)
	} else {
		p.byID[id] = q[1:]
	}
	return p.remove(q[0]), true
}

// takeOnly removes and returns the code, if exactly one waits.
func (p *pendingCodes) takeOnly() (pendingExecCode, bool) {
	if p.live != 1 {
		return pendingExecCode{}, false
	}
	p.advance()
	pc := p.items[p.head]
	if pc.id == "" {
		p.noID = 0
	} else if q := p.byID[pc.id]; len(q) == 1 {
		delete(p.byID, pc.id)
	} else {
		p.byID[pc.id] = q[1:]
	}
	return p.remove(p.head), true
}

func (p *pendingCodes) remove(pos int) pendingExecCode {
	pc := p.items[pos]
	p.items[pos] = pendingExecCode{gone: true}
	p.live--
	p.advance()
	return pc
}

// advance moves head past the items taken from the front.
func (p *pendingCodes) advance() {
	for p.head < len(p.items) && p.items[p.head].gone {
		p.head++
	}
}
