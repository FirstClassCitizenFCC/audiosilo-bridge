-- Phase 4b owner fixes: the device profile each Audible account's device
-- registered with (audible.ProfileVersion at its sign-in). Amazon denies
-- licenses to devices registered with an older profile, so an account whose
-- profile isn't the current one must sign in again; '' is a sign-in made
-- before the profile was recorded (the older profile).
ALTER TABLE audible_accounts ADD COLUMN device_profile TEXT NOT NULL DEFAULT '';
