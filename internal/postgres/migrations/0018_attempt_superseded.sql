-- An attempt a payer asked to have replaced: its key was spent by something
-- that did not pay, which the deployment cannot see, so the payer says so and
-- is given another. Superseded is not live: the one-live-per-payment index
-- names issued and confirming and leaves this out, which is what lets the
-- replacement be issued.
alter table attempts
    drop constraint attempts_status_is_one_of_the_lifecycle,
    add constraint attempts_status_is_one_of_the_lifecycle check (
        status in ('issued', 'confirming', 'superseded')
    );
