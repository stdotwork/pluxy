package geminiwebapi

import (
	"fmt"
	"html"
)

type Candidate struct {
	RCID            string
	Text            string
	Thoughts        *string
	WebImages       []WebImage
	GeneratedImages []GeneratedImage
}

func (c Candidate) String() string {
	t := c.Text
	if len(t) > 20 {
		t = t[:20] + "..."
	}
	return fmt.Sprintf("Candidate(rcid='%s', text='%s', images=%d)", c.RCID, t, len(c.WebImages)+len(c.GeneratedImages))
}

func (c Candidate) Images() []Image {
	images := make([]Image, 0, len(c.WebImages)+len(c.GeneratedImages))
	for _, wi := range c.WebImages {
		images = append(images, wi.Image)
	}
	for _, gi := range c.GeneratedImages {
		images = append(images, gi.Image)
	}
	return images
}

type ModelOutput struct {
	Metadata   []string
	Candidates []Candidate
	Chosen     int
}

func (m ModelOutput) String() string { return m.Text() }

func (m ModelOutput) Text() string {
	if len(m.Candidates) == 0 {
		return ""
	}
	return m.Candidates[m.Chosen].Text
}

func (m ModelOutput) Thoughts() *string {
	if len(m.Candidates) == 0 {
		return nil
	}
	return m.Candidates[m.Chosen].Thoughts
}

func (m ModelOutput) Images() []Image {
	if len(m.Candidates) == 0 {
		return nil
	}
	return m.Candidates[m.Chosen].Images()
}

func (m ModelOutput) RCID() string {
	if len(m.Candidates) == 0 {
		return ""
	}
	return m.Candidates[m.Chosen].RCID
}

type Gem struct {
	ID          string
	Name        string
	Description *string
	Prompt      *string
	Predefined  bool
}

func (g Gem) String() string {
	return fmt.Sprintf("Gem(id='%s', name='%s', description='%v', prompt='%v', predefined=%v)", g.ID, g.Name, g.Description, g.Prompt, g.Predefined)
}

func decodeHTML(s string) string { return html.UnescapeString(s) }
