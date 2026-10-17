package resolver

func topoSort(graph map[string]map[string]struct{}) []string {
	if len(graph) == 0 {
		return nil
	}

	unmarked := make(map[string]struct{})
	for node := range graph {
		unmarked[node] = struct{}{}
	}
	permMarks := make(map[string]struct{})
	tempMarks := make(map[string]struct{})
	var sorted []string

	var visit func(string)
	visit = func(n string) {
		if _, ok := permMarks[n]; ok {
			return
		}
		if _, ok := tempMarks[n]; ok {
			return
		}
		tempMarks[n] = struct{}{}
		for m := range graph[n] {
			visit(m)
		}
		delete(tempMarks, n)
		permMarks[n] = struct{}{}
		delete(unmarked, n)
		sorted = append(sorted, n)
	}

	for len(unmarked) > 0 {
		var n string
		for n = range unmarked {
			break
		}
		visit(n)
	}

	return sorted
}
