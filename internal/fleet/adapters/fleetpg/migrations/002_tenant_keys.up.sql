-- Defence in depth for tenant isolation: a placement, desired app or app
-- status can only point at a cluster of its own organization (every
-- statement filters by organization_id already; the schema now refuses a
-- row that would cross tenants even if one did not).
ALTER TABLE fleet.clusters ADD CONSTRAINT clusters_org_id_key UNIQUE (organization_id, id);

ALTER TABLE fleet.placements DROP CONSTRAINT placements_cluster_id_fkey,
    ADD CONSTRAINT placements_cluster_fkey FOREIGN KEY (organization_id, cluster_id) REFERENCES fleet.clusters (organization_id, id);
ALTER TABLE fleet.desired_apps DROP CONSTRAINT desired_apps_cluster_id_fkey,
    ADD CONSTRAINT desired_apps_cluster_fkey FOREIGN KEY (organization_id, cluster_id) REFERENCES fleet.clusters (organization_id, id);
ALTER TABLE fleet.app_status DROP CONSTRAINT app_status_cluster_id_fkey,
    ADD CONSTRAINT app_status_cluster_fkey FOREIGN KEY (organization_id, cluster_id) REFERENCES fleet.clusters (organization_id, id);
