#!/bin/sh
cat >&2 <<EOF
gpg: armor header: Hash: SHA512
gpg: armor header: Version: GnuPG v1
gpg: original file name=''
gpg: Signature made Fri 23 Jan 2015 04:16:00 PM GMT using RSA key ID 1343CF44
gpg: using subkey 1343CF44 instead of primary key 7FD863FE
gpg: using PGP trust model
gpg: Good signature from "Salvatore Bonaccorso <salvatore.bonaccorso@gmail.com>"
gpg:                 aka "Salvatore Bonaccorso <carnil@cpan.org>"
gpg:                 aka "Salvatore Bonaccorso <carnil@debian.org>"
gpg:                 aka "Salvatore Bonaccorso <bonaccos@ee.ethz.ch>"
gpg:                 aka "Salvatore Bonaccorso <carnil.debian@gmx.net>"
gpg:                 aka "Salvatore Bonaccorso <salvatore.bonaccorso@gmx.net>"
gpg:                 aka "Salvatore Bonaccorso <salvatore.bonaccorso@livenet.ch>"
gpg: WARNING: This key is not certified with a trusted signature!
gpg:          There is no indication that the signature belongs to the owner.
	Primary key fingerprint: 04A4 407C B914 2C23 030C  17AE 789D 6F05 7FD8 63FE
        Subkey fingerprint: 4644 4098 08C1 71E0 5531  DDEE 054C B8F3 1343 CF44
gpg: textmode signature, digest algorithm SHA512
EOF
exit 0
