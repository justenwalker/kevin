#Config: {
	// system installs into the machine-wide trust store instead of the store
	// of the user. The machine-wide store needs root, and the plugin reports
	// the command to run rather than asking for a password itself.
	system?: bool

	// firefox installs into the certificate database of every Firefox
	// profile. Firefox keeps its own database and ignores the store of the
	// operating system. The step needs certutil from the nss package.
	firefox?: bool | *true
}
