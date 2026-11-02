package watch

func init() {
	backendCases = append(backendCases, backendCase{"kqueue", newKqueue, false})
}
