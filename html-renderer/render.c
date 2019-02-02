/**
 * Wrapper for running lynx in a namespaced sandbox.
 */
/*
 *  Copyright (C) 2015 Thomas Habets <thomas@habets.se>
 *
 *  This program is free software; you can redistribute it and/or modify
 *  it under the terms of the GNU General Public License as published by
 *  the Free Software Foundation; either version 2 of the License, or
 *  (at your option) any later version.
 *
 *  This program is distributed in the hope that it will be useful,
 *  but WITHOUT ANY WARRANTY; without even the implied warranty of
 *  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *  GNU General Public License for more details.
 *
 *  You should have received a copy of the GNU General Public License along
 *  with this program; if not, write to the Free Software Foundation, Inc.,
 *  51 Franklin Street, Fifth Floor, Boston, MA 02110-1301 USA.
 */
#define _GNU_SOURCE
#include<grp.h>
#include<pwd.h>
#include<sched.h>
#include<stdio.h>
#include<sys/types.h>
#include<unistd.h>

int
main(int argc, char** argv)
{
        struct passwd* pwu;
        struct group* pwg;
        const char* user = "nobody";
        const char* group = "nogroup";

        if (!(pwu = getpwnam(user))) {
                perror("getpwnam");
                return 1;
        }
        if (!(pwg = getgrnam(group))) {
                perror("getgrnam");
                return 1;
        }
        if (initgroups(user, pwg->gr_gid)) {
                perror("initgroups");
                return 1;
        }
        if (chdir("/")) {
                perror("chdir");
                return 1;
        }
        if (unshare(CLONE_FILES
                    | CLONE_FS
                    | CLONE_NEWIPC
                    | CLONE_NEWNET
                    | CLONE_NEWNS
                    | CLONE_NEWPID
                    //| CLONE_NEWUSER // Can't do NEWUSER because then setuid doesn't work.
                    | CLONE_NEWUTS
                    | CLONE_SYSVSEM)) {
                perror("unshare");
                return 1;
        }
        if (setresgid(pwg->gr_gid, pwg->gr_gid, pwg->gr_gid)) {
                perror("setresgid");
                return 1;
        }
        if (setresuid(pwu->pw_uid, pwu->pw_uid, pwu->pw_uid)) {
                perror("setresuid");
                return 1;
        }
        execv("/usr/bin/lynx", argv);
        perror("execv");
        return 1;
}
