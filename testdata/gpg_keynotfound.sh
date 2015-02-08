#!/bin/sh
cat >&2 <<EOF
gpg: armor header: Hash: SHA512
gpg: armor header: Version: GnuPG v1
gpg: original file name=''
gpg: Signature made Fri 23 Jan 2015 04:16:00 PM GMT using RSA key ID 1343CF44
gpg: Can't check signature: public key not found
EOF
exit 1
