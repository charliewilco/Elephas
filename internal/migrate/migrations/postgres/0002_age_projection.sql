CREATE EXTENSION IF NOT EXISTS age;

LOAD 'age';
SET search_path = ag_catalog, public;

DO $$
BEGIN
  PERFORM create_graph('elephas');
EXCEPTION
  WHEN duplicate_object THEN
    NULL;
END
$$;
