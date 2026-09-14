#Config: {
	// containers narrows which container this fault impairs, by name.
	// Unset means every container every needs entry resolves to - a
	// builtin:kind cluster with no other needs entries, say, faults every
	// node identically. A name matches either a container's own name (as
	// reported by its step - see the reference doc for how each plugin
	// determines it), or, when a needs step manages exactly one
	// container, that step's own name as a fallback: "cache" works
	// whether the step reporting it is a builtin:container step or the
	// one worker of a builtin:kind cluster.
	containers?: [...string]

	// interface is the network interface inside the target container to
	// impair. Empty picks the relay's own default ("eth0").
	interface?: string

	// delay_ms is the fixed one-way delay added to every packet, in
	// milliseconds.
	delay_ms?: int & >0

	// jitter_ms is the random variation applied around delay_ms, in
	// milliseconds. Only meaningful alongside delay_ms.
	jitter_ms?: int & >=0

	// loss_percent is the percentage of packets dropped.
	loss_percent?: float & >0 & <=100

	// corrupt_percent is the percentage of packets corrupted (a single
	// bit flipped).
	corrupt_percent?: float & >0 & <=100

	// duplicate_percent is the percentage of packets duplicated.
	duplicate_percent?: float & >0 & <=100

	// reorder_percent is the percentage of packets reordered ahead of
	// delay_ms. Only meaningful alongside delay_ms.
	reorder_percent?: float & >0 & <=100
}
