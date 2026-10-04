package procuse

import "strconv"

func unescapeName(name string) (string, bool) {
	decoded := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '\r' || c == '\n' || c == 0 {
			return "", false
		}
		if c != '\\' {
			decoded = append(decoded, c)
			continue
		}
		i++
		if i == len(name) {
			return "", false
		}
		switch name[i] {
		case 'x':
			if i+2 >= len(name) {
				return "", false
			}
			value, err := strconv.ParseUint(name[i+1:i+3], 16, 8)
			if err != nil {
				return "", false
			}
			decoded = append(decoded, byte(value))
			i += 2
		case 'n':
			decoded = append(decoded, '\n')
		case 't':
			decoded = append(decoded, '\t')
		case 'r':
			decoded = append(decoded, '\r')
		case 'b':
			decoded = append(decoded, '\b')
		case 'f':
			decoded = append(decoded, '\f')
		case 'v':
			decoded = append(decoded, '\v')
		case 'a':
			decoded = append(decoded, '\a')
		case '\\':
			decoded = append(decoded, '\\')
		default:
			return "", false
		}
	}
	return string(decoded), true
}
