package geminigenerate

type pendingCodes struct {
	items []pendingExecCode
	head  int
	live  int
	byID  map[string][]int
	noID  int
}

func (p *pendingCodes) len() int { return p.live }

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

func (p *pendingCodes) dropNoID() bool {
	if p.noID == 0 {
		return false
	}
	p.remove(p.noID - 1)
	p.noID = 0
	return true
}

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

func (p *pendingCodes) advance() {
	for p.head < len(p.items) && p.items[p.head].gone {
		p.head++
	}
}
