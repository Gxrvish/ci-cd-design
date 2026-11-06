-- First start only (docker-entrypoint-initdb.d): the two CloudForge roles and their databases.
-- psql reads the passwords from the Compose secrets, so they never appear on a command line.
\set owner_password `cat /run/secrets/pg_owner_password`
\set app_password `cat /run/secrets/pg_app_password`
CREATE ROLE cloudforge_owner LOGIN PASSWORD :'owner_password';
CREATE ROLE cloudforge_app LOGIN PASSWORD :'app_password';
CREATE DATABASE cloudforge OWNER cloudforge_owner;
CREATE DATABASE cloudforge_test OWNER cloudforge_owner;
