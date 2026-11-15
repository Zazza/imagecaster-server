package tui

type scrollView struct {
	offset    int
	following bool
}

func maxScrollOffset(total, visible int) int {
	if total <= visible {
		return 0
	}
	return total - visible
}

func (s *scrollView) reset() {
	s.offset = 0
	s.following = false
}

func (s *scrollView) follow(max int) {
	s.following = true
	s.offset = max
}

func (s *scrollView) handleKey(key string, max, page int) {
	switch key {
	case "up", "k":
		s.following = false
		if s.offset > 0 {
			s.offset--
		}
	case "down", "j":
		if s.offset < max {
			s.offset++
		}
		if s.offset >= max {
			s.following = true
		}
	case "pgup":
		s.following = false
		s.offset -= page
		if s.offset < 0 {
			s.offset = 0
		}
	case "pgdown":
		s.offset += page
		if s.offset > max {
			s.offset = max
		}
		if s.offset >= max {
			s.following = true
		}
	case "g":
		s.following = false
		s.offset = 0
	case "G":
		s.follow(max)
	}
}
