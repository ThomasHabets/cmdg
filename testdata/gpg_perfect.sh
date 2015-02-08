#!/bin/sh
cat >&2 <<EOF
gpg: Signature made Fri 23 Jan 2015 04:16:00 PM GMT using RSA key ID 1343CF44
gpg: checking the trustdb
gpg: 3 marginal(s) needed, 1 complete(s) needed, PGP trust model
gpg: depth: 0  valid:   3  signed:   0  trust: 0-, 0q, 0n, 0m, 0f, 3u
gpg: next trustdb check due at 2018-02-04
gpg: Good signature from "Salvatore Bonaccorso <salvatore.bonaccorso@gmail.com>"
gpg:                 aka "Salvatore Bonaccorso <carnil@cpan.org>"
gpg:                 aka "Salvatore Bonaccorso <carnil@debian.org>"
gpg:                 aka "Salvatore Bonaccorso <bonaccos@ee.ethz.ch>"
gpg:                 aka "Salvatore Bonaccorso <carnil.debian@gmx.net>"
gpg:                 aka "Salvatore Bonaccorso <salvatore.bonaccorso@gmx.net>"
gpg:                 aka "Salvatore Bonaccorso <salvatore.bonaccorso@livenet.ch>"
EOF
exit 0
