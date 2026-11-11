package watch

func init() {
	backendCases = append(backendCases,
		backendCase{"kqueue", newKqueue, false},
		backendCase{"helper", startHelper, false}, // this test binary, run with HelperArg (TestMain)
	)
}
