import { createContext, useContext } from "react";
import type { AdminController } from "./useAdminController";

export const AdminContext = createContext<AdminController | null>(null);

export function useAdmin(): AdminController {
  const admin = useContext(AdminContext);
  if (!admin) throw new Error("useAdmin must be used inside AdminContext");
  return admin;
}
