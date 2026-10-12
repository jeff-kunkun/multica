export interface RoutingShadowResult {
  issue_id: string;
  title: string;
  actual_seat_id?: string;
  actual_seat?: string;
  reference_tier_label?: string;
  reference_tier: string;
  proposed?: { tier: string; seat_id: string; confidence: number; reason: string };
  selected?: { ID: string; Name: string; TierKey: string };
  error?: string;
  compared_at: string;
  model: string;
}
