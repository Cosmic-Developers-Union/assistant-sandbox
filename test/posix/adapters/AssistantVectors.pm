
package AssistantVectors;
use strict;
use warnings;
use JSON::PP;
sub import { no strict 'refs'; *{'main::run_tests'} = \&run_tests; }
sub run_tests {
    my ($name, $cmd, $tests) = @_;
    print JSON::PP->new->canonical->encode({ command => $cmd, tests => $tests });
    return 0;
}
1;
