#Config: {
	// containers limits the fault to these containers. Unset, the fault
	// applies to every container of every step in needs. A name is a key
	// of a builtin:kind step's workers, or the name of a step in needs that
	// has one container.
	containers?: [...string]

	// interface is the network interface in the container to impair.
	// Unset means "eth0".
	interface?: string

	// delay_ms is the fixed one-way delay added to every packet, in
	// milliseconds.
	delay_ms?: int & >0

	// jitter_ms is the random variation around delay_ms, in milliseconds.
	// Has an effect only with delay_ms.
	jitter_ms?: int & >=0

	// loss_percent is the percentage of packets dropped.
	loss_percent?: float & >0 & <=100

	// corrupt_percent is the percentage of packets corrupted (a single
	// bit flipped).
	corrupt_percent?: float & >0 & <=100

	// duplicate_percent is the percentage of packets duplicated.
	duplicate_percent?: float & >0 & <=100

	// reorder_percent is the percentage of packets sent without delay_ms,
	// ahead of delayed packets. Has an effect only with delay_ms.
	reorder_percent?: float & >0 & <=100

	// rate_kbit is the maximum throughput of the interface, in kilobits per
	// second.
	rate_kbit?: int & >0
}
